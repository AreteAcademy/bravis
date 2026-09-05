package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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

// formatoEmDisco e a primeira linha do arquivo, e o contrato.
//
// Um arquivo persistido nao se muda sem migracao, e migracao de credencial e a
// que ninguem quer fazer as pressas. A versao em texto na primeira linha e o
// que permite mudar o resto depois sem adivinhacao.
const formatoEmDisco = "brevis-cred/1"

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
	// BREVIS_CREDENTIAL_KEY. Sem chave o store RECUSA a ligar -- gravar em
	// claro seria repetir, num arquivo, o erro que acabamos de tirar de uma
	// tabela do BigQuery.
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
func (f FileStore) resolver() (*arquivoCifrado, error) {
	if strings.TrimSpace(f.Name) == "" {
		return nil, fmt.Errorf("FileStore.Name is empty: it names the file, and it must " +
			"come from you rather than from the URL -- a URL carries secrets in its " +
			"query string, and a file name reaches logs, listings and backups")
	}
	if strings.ContainsAny(f.Name, `/\`) || f.Name == "." || f.Name == ".." {
		return nil, fmt.Errorf("FileStore.Name %q is a path, and it must be a plain name", f.Name)
	}

	dir, origem := f.Dir, "FileStore.Dir"
	if dir == "" {
		dir, origem = os.Getenv(EnvCredentialDir), EnvCredentialDir
	}
	if dir == "" {
		return nil, nil
	}

	chave := f.Key
	if chave == "" {
		chave = os.Getenv(EnvCredentialKey)
	}
	if chave == "" {
		return nil, fmt.Errorf("credential store: %s is set (%s) but there is no key. "+
			"Set %s to 32 random bytes in base64 -- writing a credential in the clear "+
			"would put it somewhere backups reach, which is the thing this store exists "+
			"to stop. Generate one with: head -c 32 /dev/urandom | base64",
			origem, dir, EnvCredentialKey)
	}

	bruta, err := base64.StdEncoding.DecodeString(strings.TrimSpace(chave))
	if err != nil {
		return nil, fmt.Errorf("credential store: %s is not valid base64: %w", EnvCredentialKey, err)
	}
	if len(bruta) != 32 {
		return nil, fmt.Errorf("credential store: %s decodes to %d bytes, and AES-256 needs 32",
			EnvCredentialKey, len(bruta))
	}

	if err := prepararDiretorio(dir); err != nil {
		return nil, err
	}

	gcm, err := novoGCM(bruta)
	if err != nil {
		return nil, err
	}

	return &arquivoCifrado{caminho: filepath.Join(dir, f.Name+".cred"), gcm: gcm}, nil
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

func novoGCM(chave []byte) (cipher.AEAD, error) {
	bloco, err := aes.NewCipher(chave)
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	gcm, err := cipher.NewGCM(bloco)
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	return gcm, nil
}

type arquivoCifrado struct {
	caminho string
	gcm     cipher.AEAD
}

func (a *arquivoCifrado) Describe() string { return a.caminho }

// Load devolve o valor guardado, ou "" quando nao ha um utilizavel.
//
// Arquivo ausente, versao desconhecida, truncado ou que nao decifra sao todos
// "nao ha valor": o chamador cai na semente e a execucao segue. Falhar aqui
// seria trocar uma credencial velha por nenhuma -- e uma versao futura num
// volume compartilhado e cenario normal durante um rollout.
func (a *arquivoCifrado) Load() (string, error) {
	bruto, err := os.ReadFile(a.caminho)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("credential store: read %s: %w", a.caminho, err)
	}

	cabecalho, corpo, ok := bytes3Cut(bruto)
	if !ok || cabecalho != formatoEmDisco {
		slog.Warn("credential store: ignoring a file this version does not read",
			"file", a.caminho, "falling_back_to", "Credential.Value")
		return "", nil
	}

	n := a.gcm.NonceSize()
	if len(corpo) < n+a.gcm.Overhead() {
		slog.Warn("credential store: the stored credential is truncated",
			"file", a.caminho, "falling_back_to", "Credential.Value")
		return "", nil
	}

	claro, err := a.gcm.Open(nil, corpo[:n], corpo[n:], []byte(formatoEmDisco))
	if err != nil {
		// Chave trocada, arquivo corrompido ou adulterado. Nao se diz qual:
		// distinguir daria a quem adultera um oraculo.
		slog.Warn("credential store: the stored credential does not decrypt",
			"file", a.caminho, "falling_back_to", "Credential.Value")
		return "", nil
	}
	return string(claro), nil
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
func (a *arquivoCifrado) Save(valor string) error {
	nonce := make([]byte, a.gcm.NonceSize())
	// Sorteado a cada escrita. Reusar nonce com a mesma chave em GCM quebra a
	// cifra, e e o erro mais comum de quem faz isto pela primeira vez.
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("credential store: nonce: %w", err)
	}

	conteudo := append([]byte(formatoEmDisco+"\n"), nonce...)
	conteudo = a.gcm.Seal(conteudo, nonce, []byte(valor), []byte(formatoEmDisco))

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

// bytes3Cut separa a primeira linha do resto.
func bytes3Cut(b []byte) (string, []byte, bool) {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i]), b[i+1:], true
		}
	}
	return "", nil, false
}
