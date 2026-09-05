package to_test

import (
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
)

func lote(n int) []sdk.Envelope {
	out := make([]sdk.Envelope, n)
	for i := range out {
		out[i] = sdk.Envelope{Payload: map[string]any{"i": i, "nome": fmt.Sprintf("linha %d", i)}}
	}
	return out
}

// TestFilesDizOQueEscreveu é o defeito: o driver escolhe o nome do arquivo --
// ele carrega um carimbo de tempo, para uma segunda carga não sobrescrever a
// primeira -- e não dizia qual escolheu.
//
// Quem escreveu não sabia o que escreveu, e o log dizia "estrategia=file" sem
// dizer qual arquivo: a informação que falta às três da manhã.
func TestFilesDizOQueEscreveu(t *testing.T) {
	dir := t.TempDir()

	res, err := to.Files{Path: dir}.Write(context.Background(), lote(3), sdk.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 1 {
		t.Fatalf("Objects = %v, esperado um caminho", res.Objects)
	}

	if _, err := os.Stat(res.Objects[0]); err != nil {
		t.Errorf("o caminho reportado não existe: %v", err)
	}
	if filepath.Dir(res.Objects[0]) != dir {
		t.Errorf("o caminho %q não está no diretório configurado %q", res.Objects[0], dir)
	}
}

// TestFilesODaVoltaNoFromFiles é a prova que interessa a quem separa extract e
// load em dois passos: o caminho que sai da escrita tem de poder voltar numa
// leitura, sem ninguém remontar esquema e bucket.
func TestFilesODaVoltaNoFromFiles(t *testing.T) {
	dir := t.TempDir()

	res, err := to.Files{Path: dir}.Write(context.Background(), lote(4), sdk.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dados, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Files{Path: res.Objects[0]},
	})
	if err != nil {
		t.Fatalf("Extract do caminho reportado: %v", err)
	}
	n := 0
	for env, err := range dados.Records {
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := env.Payload.(map[string]any)["nome"]; !ok {
			t.Errorf("linha sem os campos: %v", env.Payload)
		}
		n++
	}
	if n != 4 {
		t.Errorf("releu %d linhas, escreveu 4", n)
	}
}

// TestFilesDescribeContinuaSendoODiretorio: são coisas diferentes, e um
// Describe que mudasse a cada carga deixaria de identificar o destino no log.
func TestFilesDescribeContinuaSendoODiretorio(t *testing.T) {
	f := to.Files{Path: "s3://bucket/landing/"}
	if f.Describe() != "s3://bucket/landing/" {
		t.Errorf("Describe = %q", f.Describe())
	}
}

// TestFilesComFlushEveryReportaTodos: com carga em levas são vários arquivos, e
// reportar só o último faria o passo seguinte ler um pedaço.
func TestFilesComFlushEveryReportaTodos(t *testing.T) {
	dir := t.TempDir()

	dados, err := sdk.Extract(context.Background(), sdk.Source{From: fonteDeN{10}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := sdk.Load(context.Background(), dados, sdk.Target{
		To: to.Files{Path: dir}, FlushEvery: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 4 {
		t.Fatalf("Objects = %v, esperado 4 arquivos (3+3+3+1)", res.Objects)
	}
	for _, caminho := range res.Objects {
		if _, err := os.Stat(caminho); err != nil {
			t.Errorf("%s não existe: %v", caminho, err)
		}
	}
}

type fonteDeN struct{ n int }

func (fonteDeN) Describe() string { return "fonte de teste" }
func (f fonteDeN) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		for i := 0; i < f.n; i++ {
			if !yield(sdk.Envelope{Payload: map[string]any{"i": i}}, nil) {
				return
			}
		}
	}, nil
}

// TestFilesEscreveNoDiretorioConfigurado é o defeito que o teste do caminho
// descobriu, e ele é pior que o que motivou a mudança.
//
// O ParseLocation é escrito para LEITURA, onde o último segmento sem barra é o
// nome de um objeto. No to.Files o nome do arquivo é do driver, então o Path é
// sempre diretório -- e sem isso `to.Files{Path: "s3://bucket/landing"}`
// escrevia em `s3://bucket/parte-...`, descartando o "landing" como se fosse
// nome de arquivo. Nada dizia; o arquivo aparecia um nível acima, e quem fosse
// procurá-lo no lugar configurado não acharia.
func TestFilesEscreveNoDiretorioConfigurado(t *testing.T) {
	base := t.TempDir()

	for _, sufixo := range []string{"", "/"} {
		dir := filepath.Join(base, "landing")
		res, err := to.Files{Path: dir + sufixo}.Write(
			context.Background(), lote(1), sdk.WriteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := filepath.Dir(res.Objects[0]); got != dir {
			t.Errorf("Path %q escreveu em %q, esperado %q", dir+sufixo, got, dir)
		}
	}
}

// TestFilesObjetoEmObjectStorage: com esquema, o caminho reportado é o URI
// completo -- é ele que precisa voltar num from.Files sem remontagem.
func TestFilesObjetoEmObjectStorage(t *testing.T) {
	var escritos []string
	f := to.Files{Path: "s3://meu-bucket/landing", Store: storeFalso{&escritos}}

	res, err := f.Write(context.Background(), lote(1), sdk.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 1 {
		t.Fatalf("Objects = %v", res.Objects)
	}
	if !strings.HasPrefix(res.Objects[0], "s3://meu-bucket/landing/parte-") {
		t.Errorf("caminho = %q; esperava o URI completo dentro de landing/", res.Objects[0])
	}
	// E é o mesmo que foi entregue ao store, sem esquema nem bucket.
	if len(escritos) != 1 || !strings.HasPrefix(escritos[0], "landing/parte-") {
		t.Errorf("a chave entregue ao store foi %v", escritos)
	}
}

type storeFalso struct{ chaves *[]string }

func (storeFalso) Scheme() string                                         { return "s3" }
func (storeFalso) List(context.Context, string, string) ([]string, error) { return nil, nil }
func (storeFalso) Open(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s storeFalso) Create(_ context.Context, _, chave string, r io.Reader) error {
	*s.chaves = append(*s.chaves, chave)
	_, _ = io.ReadAll(r)
	return nil
}
