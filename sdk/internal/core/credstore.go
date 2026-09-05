package core

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CredentialStore guarda o valor rotacionado entre execucoes.
//
// Duas funcoes, e de proposito: uma abstracao com uma implementacao so e um
// palpite sobre a segunda. Quando existir um store em rede, o formato dele vai
// ensinar coisas que hoje seriam adivinhadas -- o que se faz agora e nao
// impedir, mantendo leitura e escrita atras destas duas.
type CredentialStore interface {
	// Load devolve o valor guardado. Ausente devolve ("", nil): nao ha valor
	// nao e erro, e o chamador cai na semente.
	Load() (string, error)

	// Save grava o valor rotacionado.
	Save(value string) error

	// Describe nomeia o store para log, sem revelar nada do valor.
	Describe() string
}

// CredentialStoreChecker e implementado por um store que sabe recusar
// configuracao invalida antes da execucao comecar.
//
// Opcional para que um store de terceiro nao precise implementa-lo, e usado
// por Credential.Check: descobrir que a credencial nao seria guardada DEPOIS
// da carga inteira e tarde demais para agir.
type CredentialStoreChecker interface {
	CheckStore() error
}

// Nomes das variaveis que a plataforma injeta.
const (
	EnvCredentialDir = "BREVIS_CREDENTIAL_DIR"
	EnvCredentialKey = "BREVIS_CREDENTIAL_KEY"
)

// FileStore guarda a credencial num arquivo cifrado dentro de um diretorio que
// alguem forneceu.
//
// O SDK nao aprende Kubernetes, nem GCS, nem banco: ele abre um arquivo. Quem
// monta o volume e problema da plataforma, e e isso que deixa a mesma feature
// rodar em ./.brevis na maquina de alguem.
type FileStore struct {
	// Name e o nome do arquivo, sem extensao. Obrigatorio.
	//
	// Vem do chamador e nunca da URL: URL carrega segredo em query string, e
	// nome de arquivo vaza para log, listagem e backup.
	Name string

	// Dir e o diretorio. Vazio consulta BREVIS_CREDENTIAL_DIR; vazio nos dois
	// desliga o store, dizendo no log que desligou.
	Dir string

	// Key e a chave de 32 bytes em base64. Vazia consulta
	// BREVIS_CREDENTIAL_KEY; vazia nos dois grava em claro, dizendo uma vez
	// no log que esta em claro.
	//
	// Para um diretorio a recomendacao e USAR: um diretorio e mais facil de
	// acabar compartilhado do que um bucket com IAM. O que protege quando nao
	// ha chave e a permissao 0700, e so.
	Key string
}

// CheckStore satisfaz CredentialStoreChecker: recusa na montagem, e loga uma
// vez quando o store fica desligado.
func (f FileStore) CheckStore() error {
	arq, err := f.resolver()
	if err != nil {
		return err
	}
	if arq == nil {
		slog.Info("credential store is off",
			"reason", "neither FileStore.Dir nor "+EnvCredentialDir+" is set",
			"effect", "the rotated credential lives for this run only")
		return nil
	}
	WarnIfPlaintext(arq.env, arq.caminho)
	slog.Debug("credential store is on", "file", arq.caminho)
	return nil
}

// Describe nomeia o arquivo, nunca o conteudo.
func (f FileStore) Describe() string {
	dir := f.Dir
	if dir == "" {
		dir = os.Getenv(EnvCredentialDir)
	}
	if dir == "" {
		return "file store (off)"
	}
	return filepath.Join(dir, f.Name+".cred")
}

// Load devolve o valor guardado, ou "" quando o store esta desligado.
func (f FileStore) Load() (string, error) {
	arq, err := f.resolver()
	if err != nil || arq == nil {
		return "", err
	}
	return arq.Load()
}

// Save grava o valor. Com o store desligado nao faz nada e nao reclama: quem
// nao configurou diretorio ja foi avisado uma vez, na montagem.
func (f FileStore) Save(valor string) error {
	arq, err := f.resolver()
	if err != nil || arq == nil {
		return err
	}
	return arq.Save(valor)
}

// resolver resolve diretorio e chave.
//
// Devolve (nil, nil) quando nao ha diretorio: o store desligado e um estado
// normal -- e como a feature continua sendo atalho e nao requisito.
func (f FileStore) resolver() (*arquivoDeCredencial, error) {
	if strings.TrimSpace(f.Name) == "" {
		return nil, fmt.Errorf("FileStore.Name is empty: it names the file, and it must " +
			"come from you rather than from the URL -- a URL carries secrets in its " +
			"query string, and a file name reaches logs, listings and backups")
	}
	if strings.ContainsAny(f.Name, `/\`) || f.Name == "." || f.Name == ".." {
		return nil, fmt.Errorf("FileStore.Name %q is a path, and it must be a plain name", f.Name)
	}

	dir := f.Dir
	if dir == "" {
		dir = os.Getenv(EnvCredentialDir)
	}
	if dir == "" {
		return nil, nil
	}

	chave := f.Key
	if chave == "" {
		chave = os.Getenv(EnvCredentialKey)
	}
	env, err := NewCredentialBox(chave)
	if err != nil {
		return nil, err
	}

	if err := prepararDiretorio(dir); err != nil {
		return nil, err
	}

	return &arquivoDeCredencial{caminho: filepath.Join(dir, f.Name+".cred"), env: env}, nil
}

// prepararDiretorio cria o diretorio a 0700, e recusa um que ja exista com
// permissao mais frouxa: um volume compartilhado com 0777 e um diretorio
// publico, e guardar credencial nele nao e melhor que nao guardar.
func prepararDiretorio(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("credential store: create %s: %w", dir, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("credential store: %s: %w", dir, err)
	case !info.IsDir():
		return fmt.Errorf("credential store: %s is not a directory", dir)
	}

	if modo := info.Mode().Perm(); modo&0o077 != 0 {
		return fmt.Errorf("credential store: %s is mode %04o, and anyone on the host or "+
			"the shared volume can read what goes in it. Use 0700 -- `chmod 700 %s` on a "+
			"local directory, or mountOptions on the volume (for gcsfuse: "+
			"dir-mode=0700,file-mode=0600)", dir, modo, dir)
	}
	return nil
}

type arquivoDeCredencial struct {
	caminho string
	env     CredentialBox
}

func (a *arquivoDeCredencial) Describe() string { return a.caminho }

// Load devolve o valor guardado, ou "" quando nao ha um utilizavel.
//
// Arquivo ausente e "nao ha valor", nao erro: e a primeira execucao de todas.
func (a *arquivoDeCredencial) Load() (string, error) {
	bruto, err := os.ReadFile(a.caminho)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("credential store: read %s: %w", a.caminho, err)
	}
	return a.env.Open(bruto, a.caminho), nil
}

// Save grava o valor, cifrado, de forma atomica.
//
// Temporario no MESMO diretorio e rename: um pod morto no meio da escrita nao
// pode deixar um arquivo pela metade, que decifraria com erro e mandaria o
// proximo run para a semente em silencio.
//
// Ultimo a escrever vence, e isso e uma escolha e nao um descuido: verificado
// no fornecedor que motivou esta feature que rotacionar NAO invalida o token
// anterior, entao dois pods renovando ao mesmo tempo gravam dois valores que
// ambos funcionam. Para um fornecedor que invalide o anterior, isto nao serve
// -- e a advertencia esta na doc de Refresh.Store.
func (a *arquivoDeCredencial) Save(valor string) error {
	conteudo, err := a.env.Seal(valor)
	if err != nil {
		return err
	}

	dir := filepath.Dir(a.caminho)
	tmp, err := os.CreateTemp(dir, ".cred-*")
	if err != nil {
		return fmt.Errorf("credential store: temp file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op quando o rename deu certo

	// Sem Chmod: os.CreateTemp ja cria com 0600, e um chmod explicito seria um
	// no-op num gcsfuse -- ou um erro, dependendo da montagem.
	if _, err := tmp.Write(conteudo); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credential store: write: %w", err)
	}
	// Sync antes do rename: sem ele, o rename pode chegar ao disco antes do
	// conteudo, e uma queda deixa um arquivo vazio no lugar de um valido.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credential store: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credential store: close: %w", err)
	}

	if err := os.Rename(tmp.Name(), a.caminho); err != nil {
		return fmt.Errorf("credential store: rename into place: %w", err)
	}
	return nil
}
