package sdk

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/from"
)

// destinoFalso é um Writer que devolve resultado e erro juntos, que é o que
// todo destino faz numa recusa -- o resultado existe para que RowErrors seja
// legível depois dela.
type destinoFalso struct {
	falha bool
	rows  []string
}

func (destinoFalso) Describe() string { return "destino.teste" }

func (d destinoFalso) Write(context.Context, []Envelope, WriteOptions) (*LoadResult, error) {
	res := &LoadResult{RowsLoaded: 2, ErrorRows: d.rows}
	if d.falha {
		res.RowsLoaded = 0
		return res, context.DeadlineExceeded
	}
	return res, nil
}

func rodaCapturandoLog(t *testing.T, destino Writer) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1},{"id":2}]`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(anterior)

	_ = runPipeline(context.Background(), &Pipeline{
		Source: Source{From: from.HTTP{URL: srv.URL}},
		Target: Target{To: destino},
	})

	return buf.String()
}

// Uma carga que não carregou não pode logar "loaded".
//
// O resultado volta preenchido no caminho de erro por desenho, para que
// RowErrors seja legível -- então a mensagem é a única coisa que distingue os
// dois casos. E "loaded" em INFO numa falha não chega a quem observa ERROR.
func TestLogNaoDizLoadedQuandoNaoCarregou(t *testing.T) {
	saida := rodaCapturandoLog(t, destinoFalso{falha: true, rows: []string{"linha 0 recusada"}})

	if strings.Contains(saida, "msg=loaded") {
		t.Errorf("uma carga que falhou logou \"loaded\":\n%s", saida)
	}
	if !strings.Contains(saida, `msg="load failed"`) {
		t.Errorf("a falha precisa aparecer na linha que resume:\n%s", saida)
	}
	if !strings.Contains(saida, "level=ERROR msg=\"load failed\"") {
		t.Errorf("a linha que resume uma falha tem de ser ERROR:\n%s", saida)
	}
	// E os contadores continuam ali, que é o motivo de o resultado voltar.
	if !strings.Contains(saida, "lines=0") || !strings.Contains(saida, "records=2") {
		t.Errorf("os contadores se perderam na troca:\n%s", saida)
	}
	if !strings.Contains(saida, "row rejected") {
		t.Errorf("as linhas recusadas sumiram:\n%s", saida)
	}
}

func TestLogDizLoadedQuandoCarregou(t *testing.T) {
	saida := rodaCapturandoLog(t, destinoFalso{})

	if !strings.Contains(saida, "level=INFO msg=loaded") {
		t.Errorf("uma carga que funcionou tem de logar loaded em INFO:\n%s", saida)
	}
	if strings.Contains(saida, "load failed") {
		t.Errorf("uma carga que funcionou não pode logar falha:\n%s", saida)
	}
}
