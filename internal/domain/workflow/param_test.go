package workflow_test

import (
	"strings"
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

func comParams(ps ...wf.Param) wf.Workflow {
	return wf.Workflow{
		Slug: "w", Params: ps,
		Nodes: []wf.Node{{ID: "a", Run: "echo"}},
	}
}

func TestResolverUsaOPadraoEOInformado(t *testing.T) {
	w := comParams(
		wf.Param{Nome: "load_full", Tipo: wf.ParamBool, Padrao: "false"},
		wf.Param{Nome: "days", Tipo: wf.ParamInteiro, Padrao: "7"},
	)

	out, err := w.Resolver(map[string]string{"load_full": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if out["load_full"] != "true" {
		t.Errorf("informado ignorado: %v", out)
	}
	if out["days"] != "7" {
		t.Errorf("padrao perdido: %v", out)
	}
}

// Chave desconhecida e ERRO, nao silencio: `--param lod_full=true` com typo
// rodaria com o padrao e ninguem perceberia que o backfill nao aconteceu.
func TestParamDesconhecidoEhRecusado(t *testing.T) {
	w := comParams(wf.Param{Nome: "load_full", Tipo: wf.ParamBool, Padrao: "false"})

	_, err := w.Resolver(map[string]string{"lod_full": "true"})
	if err == nil {
		t.Fatal("typo no nome do param passou despercebido")
	}
	if !strings.Contains(err.Error(), "load_full") {
		t.Errorf("o erro nao diz quais existem: %v", err)
	}
}

func TestTiposSaoValidados(t *testing.T) {
	casos := []struct {
		param wf.Param
		valor string
		ok    bool
	}{
		{wf.Param{Nome: "b", Tipo: wf.ParamBool}, "true", true},
		{wf.Param{Nome: "b", Tipo: wf.ParamBool}, "sim", false},
		{wf.Param{Nome: "n", Tipo: wf.ParamInteiro}, "42", true},
		{wf.Param{Nome: "n", Tipo: wf.ParamInteiro}, "42.5", false},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto}, "2026-09-01", true},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Enum: []string{"a", "b"}}, "a", true},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Enum: []string{"a", "b"}}, "c", false},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Pattern: `^\d{4}-\d{2}-\d{2}$`}, "2026-09-01", true},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Pattern: `^\d{4}-\d{2}-\d{2}$`}, "ontem", false},
	}
	for _, c := range casos {
		err := c.param.Aceita(c.valor)
		if c.ok && err != nil {
			t.Errorf("%s=%q recusado: %v", c.param.Tipo, c.valor, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s=%q aceito e nao deveria", c.param.Tipo, c.valor)
		}
	}
}

// O valor de um param vai PARA DENTRO da linha de comando do passo, e quem
// dispara um run nao e necessariamente quem escreveu o workflow. Sem esta
// barreira, `--date {{ .data }}` com `; rm -rf /` seria execucao arbitraria no
// worker.
func TestTextoRecusaCaractereDeShell(t *testing.T) {
	p := wf.Param{Nome: "data", Tipo: wf.ParamTexto}

	for _, veneno := range []string{
		"; rm -rf /",
		"$(whoami)",
		"`id`",
		"a | cat /etc/passwd",
		"a && curl http://malicioso",
		"a > /tmp/x",
		"'; DROP TABLE runs; --",
	} {
		if err := p.Aceita(veneno); err == nil {
			t.Errorf("aceitou %q", veneno)
		}
	}

	// E continua servindo para o que os params reais precisam.
	for _, legitimo := range []string{
		"2026-09-01", "bronze_id_verification+", "true", "path/to/file.csv",
		"a,b,c", "chave=valor", "50", "us-central1",
	} {
		if err := p.Aceita(legitimo); err != nil {
			t.Errorf("recusou valor legitimo %q: %v", legitimo, err)
		}
	}
}

// Quem precisa mesmo de um caractere fora do conjunto declara `pattern` — a
// decisao passa a ser explicita, do autor do workflow.
func TestPatternAmpliaOQueEhAceito(t *testing.T) {
	p := wf.Param{Nome: "json", Tipo: wf.ParamTexto, Pattern: `^\{"[a-z_]+":"[a-z]+"\}$`}
	if err := p.Aceita(`{"load_full":"true"}`); err != nil {
		t.Errorf("pattern do autor foi ignorado: %v", err)
	}
}

// Default invalido so apareceria no primeiro disparo agendado, de madrugada.
func TestPadraoInvalidoFalhaNaPublicacao(t *testing.T) {
	w := comParams(wf.Param{Nome: "days", Tipo: wf.ParamInteiro, Padrao: "muitos"})
	if err := w.Validate(); err == nil {
		t.Fatal("valor padrao invalido passou na validacao")
	}
}

func TestDeclaracaoInvalidaEhRecusada(t *testing.T) {
	casos := []wf.Param{
		{Nome: "Load_Full", Tipo: wf.ParamBool},          // maiuscula
		{Nome: "2days", Tipo: wf.ParamInteiro},           // comeca com digito
		{Nome: "ok", Tipo: "float"},                      // tipo inexistente
		{Nome: "ok"},                                     // sem tipo
		{Nome: "ok", Tipo: wf.ParamTexto, Pattern: "[("}, // regex quebrada
	}
	for _, p := range casos {
		if err := p.Validar(); err == nil {
			t.Errorf("declaracao invalida aceita: %+v", p)
		}
	}
}

func TestParamDuplicadoEhRecusado(t *testing.T) {
	w := comParams(
		wf.Param{Nome: "x", Tipo: wf.ParamTexto},
		wf.Param{Nome: "x", Tipo: wf.ParamBool},
	)
	if err := w.Validate(); err == nil {
		t.Fatal("param duplicado passou")
	}
}
