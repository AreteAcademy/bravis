package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chaveDeTeste(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func storePronto(t *testing.T) (FileStore, string) {
	t.Helper()
	// t.TempDir vem 0755 nesta plataforma, e o store recusa diretorio frouxo
	// -- que e o comportamento pedido, entao o teste se ajusta a ele.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))
	return FileStore{Name: "gabriel-session"}, dir
}

// TestGuardaEDevolve: o caminho feliz, e o unico que o consumidor vai ver.
func TestGuardaEDevolve(t *testing.T) {
	s, dir := storePronto(t)

	const valor = "session=eyJhbGciOiJkaXIi..QUJDRA=="
	if err := s.Save(valor); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != valor {
		t.Errorf("Load = %q, esperado %q", got, valor)
	}

	// E o valor nao esta em claro no arquivo.
	bruto, err := os.ReadFile(filepath.Join(dir, "gabriel-session.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bruto), "eyJhbGciOiJkaXIi") {
		t.Error("a credencial esta em claro no arquivo")
	}
	if !strings.HasPrefix(string(bruto), formatoCifrado+"\n") {
		t.Errorf("o arquivo nao comeca com a versao: %q", bruto[:min(20, len(bruto))])
	}
}

// TestAusenteNaoEErro: nao ha valor guardado e um estado normal -- a primeira
// execucao de todas. O chamador cai na semente.
func TestAusenteNaoEErro(t *testing.T) {
	s, _ := storePronto(t)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load de arquivo ausente devolveu erro: %v", err)
	}
	if got != "" {
		t.Errorf("Load = %q, esperado vazio", got)
	}
}

// TestNonceNaoSeRepete: reusar nonce com a mesma chave em GCM quebra a cifra, e
// e o erro mais comum de quem implementa isso pela primeira vez.
func TestNonceNaoSeRepete(t *testing.T) {
	s, dir := storePronto(t)

	vistos := map[string]bool{}
	for i := 0; i < 50; i++ {
		if err := s.Save("mesmo-valor-sempre"); err != nil {
			t.Fatal(err)
		}
		bruto, err := os.ReadFile(filepath.Join(dir, "gabriel-session.cred"))
		if err != nil {
			t.Fatal(err)
		}
		_, corpo, _ := primeiraLinha(bruto)
		nonce := string(corpo[:12])
		if vistos[nonce] {
			t.Fatalf("nonce repetido na escrita %d", i)
		}
		vistos[nonce] = true
	}
}

// TestSemChaveGravaEmClaro: a cifra e opcional. O controle de verdade e o do
// storage -- permissao do diretorio, IAM do bucket -- e uma chave que vive no
// mesmo secret de quem le o store nao protege de ninguem.
func TestSemChaveGravaEmClaro(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, "")

	s := FileStore{Name: "x"}
	if err := s.CheckStore(); err != nil {
		t.Fatalf("sem chave virou erro: %v", err)
	}

	const valor = "session=abc=="
	if err := s.Save(valor); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil || got != valor {
		t.Fatalf("Load = (%q, %v)", got, err)
	}

	bruto, err := os.ReadFile(filepath.Join(dir, "x.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(bruto), formatoClaro+"\n") {
		t.Errorf("o claro tambem precisa de versao na primeira linha: %q", bruto)
	}
}

// TestClaroAvisaUmaVezSo: um aviso repetido a cada pipeline vira ruido, e
// ruido e como um aviso deixa de ser lido.
func TestClaroAvisaUmaVezSo(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, "")
	avisos.Delete(filepath.Join(dir, "x.cred"))

	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(anterior)

	s := FileStore{Name: "x"}
	for i := 0; i < 5; i++ {
		if err := s.CheckStore(); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "writing in the clear"); n != 1 {
		t.Errorf("avisou %d vezes, esperado 1", n)
	}
}

// TestComChaveNaoGravaEmClaro: e o outro lado -- a opcao de cifrar tem de
// realmente cifrar.
func TestComChaveNaoGravaEmClaro(t *testing.T) {
	s, dir := storePronto(t)
	if err := s.Save("session=abc=="); err != nil {
		t.Fatal(err)
	}
	bruto, err := os.ReadFile(filepath.Join(dir, "gabriel-session.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bruto), "session=abc==") {
		t.Error("com chave, gravou em claro")
	}
	if !strings.HasPrefix(string(bruto), formatoCifrado+"\n") {
		t.Errorf("cabecalho errado: %q", bruto[:min(20, len(bruto))])
	}
}

// TestCifradoSemChaveCaiNaSemente: durante um rollout, ou depois de alguem
// remover a chave, o store tem um valor que este processo nao le. Cair na
// semente e o certo; devolver lixo seria pior.
func TestCifradoSemChaveCaiNaSemente(t *testing.T) {
	s, dir := storePronto(t)
	if err := s.Save("segredo"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialKey, "")

	got, err := FileStore{Name: "gabriel-session"}.Load()
	if err != nil {
		t.Fatalf("virou erro: %v", err)
	}
	if got != "" {
		t.Errorf("Load = %q, esperado vazio", got)
	}
	_ = dir
}

// TestChaveDeTamanhoErrado: AES-256 quer 32 bytes, e uma chave curta falharia
// mais tarde com uma mensagem sobre tamanho de bloco. Chave presente e ruim e
// erro; chave AUSENTE e escolha.
func TestChaveDeTamanhoErrado(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	for _, ruim := range []string{
		base64.StdEncoding.EncodeToString([]byte("curta")),
		"isto nao e base64!!",
	} {
		t.Setenv(EnvCredentialKey, ruim)
		if err := (FileStore{Name: "x"}).CheckStore(); err == nil {
			t.Errorf("aceitou a chave %q", ruim)
		}
	}
}

// TestSemDiretorioODesligaEmVezDeFalhar: sem Dir e sem a env, o comportamento e
// exatamente o de antes da feature -- e assim ela continua sendo atalho, nao
// requisito.
func TestSemDiretorioODesligaEmVezDeFalhar(t *testing.T) {
	t.Setenv(EnvCredentialDir, "")
	t.Setenv(EnvCredentialKey, "")

	s := FileStore{Name: "x"}
	if err := s.CheckStore(); err != nil {
		t.Fatalf("store desligado virou erro: %v", err)
	}
	if v, err := s.Load(); err != nil || v != "" {
		t.Errorf("Load desligado = (%q, %v)", v, err)
	}
	if err := s.Save("qualquer"); err != nil {
		t.Errorf("Save desligado = %v", err)
	}
}

// TestDiretorioFrouxoERecusado: um volume compartilhado com 0777 e um diretorio
// publico, e guardar credencial nele nao e melhor que nao guardar.
func TestDiretorioFrouxoERecusado(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))

	err := FileStore{Name: "x"}.CheckStore()
	if err == nil {
		t.Fatal("diretorio 0777 foi aceito")
	}
	if !strings.Contains(err.Error(), "0700") {
		t.Errorf("o erro nao diz o que fazer: %v", err)
	}
}

// TestPermissoes: arquivo 0600, diretorio 0700.
func TestPermissoes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "criado-por-mim")
	t.Setenv(EnvCredentialDir, dir)
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))

	s := FileStore{Name: "x"}
	if err := s.Save("v"); err != nil {
		t.Fatal(err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("diretorio = %04o, esperado 0700", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "x.cred"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("arquivo = %04o, esperado 0600", got)
	}
}

// TestArquivoIlegivelCaiNaSemente: versao futura, truncado, ou chave trocada.
// Nenhum e erro: falhar trocaria uma credencial velha por nenhuma, e uma versao
// futura num volume compartilhado e cenario normal durante um rollout.
func TestArquivoIlegivelCaiNaSemente(t *testing.T) {
	casos := map[string][]byte{
		"versao futura": []byte("brevis-cred/9\nqualquer coisa aqui dentro"),
		"sem versao":    []byte("nao tem newline nenhum"),
		"truncado":      []byte(formatoCifrado + "\ncurto"),
		"nao decifra":   append([]byte(formatoCifrado+"\n"), make([]byte, 60)...),
		"arquivo vazio": {},
	}
	for nome, conteudo := range casos {
		t.Run(nome, func(t *testing.T) {
			s, dir := storePronto(t)
			if err := os.WriteFile(filepath.Join(dir, "gabriel-session.cred"), conteudo, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load()
			if err != nil {
				t.Fatalf("Load virou erro em vez de cair na semente: %v", err)
			}
			if got != "" {
				t.Errorf("Load = %q, esperado vazio", got)
			}
		})
	}
}

// TestChaveTrocadaNaoDecifra: e o teste que prova que a cifra e a chave, nao
// so um encode.
func TestChaveTrocadaNaoDecifra(t *testing.T) {
	// t.TempDir vem 0755 nesta plataforma, e o store recusa diretorio frouxo
	// -- que e o comportamento pedido, entao o teste se ajusta a ele.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCredentialDir, dir)

	t.Setenv(EnvCredentialKey, chaveDeTeste(t))
	if err := (FileStore{Name: "x"}).Save("segredo"); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvCredentialKey, chaveDeTeste(t))
	got, err := FileStore{Name: "x"}.Load()
	if err != nil {
		t.Fatalf("chave trocada virou erro: %v", err)
	}
	if got != "" {
		t.Errorf("decifrou com outra chave: %q", got)
	}
}

// TestAdulteracaoEDetectada: GCM autentica. Um byte trocado no texto cifrado
// tem de invalidar, e nao produzir lixo que vira credencial.
func TestAdulteracaoEDetectada(t *testing.T) {
	s, dir := storePronto(t)
	if err := s.Save("segredo"); err != nil {
		t.Fatal(err)
	}
	caminho := filepath.Join(dir, "gabriel-session.cred")
	bruto, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	bruto[len(bruto)-1] ^= 0xff
	if err := os.WriteFile(caminho, bruto, 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := s.Load(); err != nil || got != "" {
		t.Errorf("adulteracao passou: (%q, %v)", got, err)
	}
}

// TestNomeQueEUmCaminhoERecusado: o nome vem do chamador e nunca da URL, e
// tambem nao pode escapar do diretorio.
func TestNomeQueEUmCaminhoERecusado(t *testing.T) {
	t.Setenv(EnvCredentialDir, t.TempDir())
	t.Setenv(EnvCredentialKey, chaveDeTeste(t))

	for _, ruim := range []string{"", "../fora", "sub/dir", ".", ".."} {
		if err := (FileStore{Name: ruim}).CheckStore(); err == nil {
			t.Errorf("aceitou Name = %q", ruim)
		}
	}
}

// TestEscritasConcorrentesNaoCorrompem: ultimo a escrever vence, e e escolha
// documentada -- mas o arquivo tem de continuar legivel, nunca meio escrito.
func TestEscritasConcorrentesNaoCorrompem(t *testing.T) {
	s, _ := storePronto(t)

	pronto := make(chan struct{})
	fim := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { fim <- struct{}{} }()
			<-pronto
			for j := 0; j < 30; j++ {
				if err := s.Save(strings.Repeat("v", i+1)); err != nil {
					t.Errorf("Save: %v", err)
					return
				}
				got, err := s.Load()
				if err != nil {
					t.Errorf("Load: %v", err)
					return
				}
				// Qualquer um dos oito valores serve; o que nao pode e lixo.
				if got != "" && strings.Trim(got, "v") != "" {
					t.Errorf("Load devolveu conteudo corrompido: %q", got)
					return
				}
			}
		}(i)
	}
	close(pronto)
	for i := 0; i < 8; i++ {
		<-fim
	}
}

// TestOStoreVemAntesDaSemente: e a ordem que faz a feature valer. Um valor
// guardado e o resultado da ultima rotacao; a semente e o que alguem colou uma
// vez, e pode ja ter vencido.
func TestOStoreVemAntesDaSemente(t *testing.T) {
	s, _ := storePronto(t)
	if err := s.Save("do-store"); err != nil {
		t.Fatal(err)
	}

	var sementeUsada bool
	c := &Credential{
		Value:   func(context.Context) (string, error) { sementeUsada = true; return "da-semente", nil },
		Apply:   AsCookie,
		Refresh: &Refresh{URL: "http://x", Store: s},
	}

	got, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "do-store" {
		t.Errorf("Get = %q, esperado o valor do store", got)
	}
	if sementeUsada {
		t.Error("a semente foi chamada mesmo havendo valor guardado")
	}
}

// TestSemValorGuardadoUsaASemente: a primeira execucao de todas.
func TestSemValorGuardadoUsaASemente(t *testing.T) {
	s, _ := storePronto(t)

	c := &Credential{
		Value:   func(context.Context) (string, error) { return "da-semente", nil },
		Apply:   AsCookie,
		Refresh: &Refresh{URL: "http://x", Store: s},
	}
	got, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "da-semente" {
		t.Errorf("Get = %q, esperado a semente", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
