package sdk

import (
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

func aplica(t *testing.T, fn Transformer, in map[string]any) map[string]any {
	t.Helper()
	got, err := fn(in)
	if err != nil {
		t.Fatalf("transformer: %v", err)
	}
	return got.(map[string]any)
}

// O critério que segura tudo: o id tem de ser o mesmo que o bloco produzia.
// Se ele mudar, toda carga anterior de todo consumidor deixa de casar.
//
// O valor esperado vem do Envelope.IngestionID, que é a implementação que
// existia antes e continua conferida byte a byte contra o uuid.uuid5 do
// Python.
func TestIngestionIDProduzOMesmoIDDeAntes(t *testing.T) {
	env := core.Envelope{
		Provider: "open_meteo", Entity: "hourly_temperature",
		SourceKey: "-23.55|-46.63|2026-01-01T00:00", RecordTS: "2026-01-01T00:00",
	}
	esperado, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}

	got := aplica(t, IngestionID(), map[string]any{
		"provider": "open_meteo", "entity": "hourly_temperature",
		"source_key": "-23.55|-46.63|2026-01-01T00:00", "record_ts": "2026-01-01T00:00",
	})

	if got[ColumnIngestionID] != esperado {
		t.Errorf("o id mudou de fórmula:\n  transformer: %v\n  antes:       %v",
			got[ColumnIngestionID], esperado)
	}
}

// E contra o valor congelado, escrito por extenso: um teste que compara duas
// implementações passa se as duas mudarem juntas.
func TestIngestionIDContraOValorCongelado(t *testing.T) {
	got := aplica(t, IngestionID(), map[string]any{
		"provider": "p", "entity": "e", "source_key": "k", "record_ts": "2026-01-01T00:00:00Z",
	})

	// Conferido contra o uuid.uuid5 do Python, que é a paridade que importa:
	//   uuid.uuid5(UUID("e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"),
	//              "p|e|k|2026-01-01T00:00:00Z")
	const congelado = "178d0b49-dece-5738-b8eb-f5cae222a1ea"
	if got[ColumnIngestionID] != congelado {
		t.Errorf("ingestion_id = %v, congelado em %s", got[ColumnIngestionID], congelado)
	}
}

func TestIngestionIDLeOsNomesQueVoceDer(t *testing.T) {
	comCanonicos := aplica(t, IngestionID(), map[string]any{
		"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t",
	})
	comOutros := aplica(t, IngestionID("provider", "entity", "source_key", "time"),
		map[string]any{"provider": "p", "entity": "e", "source_key": "k", "time": "t"})

	if comCanonicos[ColumnIngestionID] != comOutros[ColumnIngestionID] {
		t.Error("nomear os campos tem de dar o mesmo id que os canônicos")
	}
}

// Campo nomeado e ausente é erro nomeando o campo -- geralmente significa que
// a cadeia está fora de ordem, ou que o Without correu antes.
func TestIngestionIDRecusaCampoAusente(t *testing.T) {
	_, err := IngestionID()(map[string]any{"provider": "p", "entity": "e"})
	if err == nil {
		t.Fatal("faltando source_key e record_ts, o id seria construído de vazio")
	}
	for _, quer := range []string{"source_key", "record_ts"} {
		if !strings.Contains(err.Error(), quer) {
			t.Errorf("o erro não nomeia %q: %v", quer, err)
		}
	}
	// E diz o que a linha tem, para o conserto sair de uma leitura.
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("o erro não lista o que a linha tem: %v", err)
	}
}

// source_key vazio é o caso que o Envelope.IngestionID já recusava: sem ele
// não há identidade estável, e o id mudaria a cada execução.
func TestIngestionIDRecusaSourceKeyVazio(t *testing.T) {
	_, err := IngestionID()(map[string]any{
		"provider": "p", "entity": "e", "source_key": "", "record_ts": "t",
	})
	if err == nil {
		t.Fatal("source_key vazio não dá identidade estável")
	}
}

func TestIngestionIDRecusaSobrescrever(t *testing.T) {
	_, err := IngestionID()(map[string]any{
		"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t",
		"ingestion_id": "meu-proprio",
	})
	if err == nil {
		t.Fatal("sobrescrever um id que a linha já tem seria invisível")
	}
}

func TestIngestionIDRecusaNumeroErradoDeCampos(t *testing.T) {
	_, err := IngestionID("provider", "entity")(map[string]any{"provider": "p", "entity": "e"})
	if err == nil {
		t.Fatal("a fórmula tem quatro componentes; dois não dá")
	}
	if !strings.Contains(err.Error(), "4") {
		t.Errorf("o erro precisa dizer quantos: %v", err)
	}
}

func TestIngestionLoadedAtEscreveAgoraEmUTC(t *testing.T) {
	got := aplica(t, IngestionLoadedAt(), map[string]any{"a": 1})

	v, ok := got[ColumnIngestionLoadedAt].(string)
	if !ok {
		t.Fatalf("ingestion_loaded_at = %T", got[ColumnIngestionLoadedAt])
	}
	quando, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatalf("não é RFC 3339: %q", v)
	}
	if d := time.Since(quando); d > time.Minute || d < -time.Minute {
		t.Errorf("ingestion_loaded_at = %v, esperado agora", quando)
	}
	if !strings.HasSuffix(v, "Z") {
		t.Errorf("esperado UTC: %q", v)
	}
	if got["a"] != 1 {
		t.Error("o resto da linha se perdeu")
	}
}

func TestIngestionLoadedAtRecusaSobrescrever(t *testing.T) {
	_, err := IngestionLoadedAt()(map[string]any{"ingestion_loaded_at": "ontem"})
	if err == nil {
		t.Fatal("sobrescrever o instante da carga seria invisível")
	}
}

// TestTransformersEscrevemNoLugar fixa o contrato NOVO, e ele é o oposto do
// que este teste afirmava antes.
//
// Cada transformer devolvia um mapa novo, "porque o chamador ainda pode estar
// segurando o mapa". Isso é verdade uma vez -- para o mapa que o decodificador
// entregou --, e as outras seis cópias por registro eram trabalho idêntico
// repetido. A cópia passou a ser feita uma vez, no `applyAll`.
//
// Quem chama um transformer SOZINHO, fora da cadeia, passa a ver a própria
// linha alterada. Está documentado no tipo Transformer, e é o preço da conta
// que o teste seguinte mede.
func TestTransformersEscrevemNoLugar(t *testing.T) {
	linha := map[string]any{"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t"}
	saida := aplica(t, IngestionID(), linha)

	if _, ok := linha[ColumnIngestionID]; !ok {
		t.Error("o transformer devolveu um mapa novo; a economia da cadeia depende de ele escrever no lugar")
	}
	if fmt.Sprint(saida) != fmt.Sprint(linha) {
		t.Error("o que voltou não é o mesmo mapa que entrou")
	}
}

// TestTransformNaoMutaOQueOExtractEntregou é a garantia que passou a importar,
// e que antes não existia como teste.
//
// O preview do extract guarda o registro que a FONTE mandou, para mostrar
// exatamente isso. Se a cadeia escrevesse por cima dele, o preview passaria a
// mostrar o resultado do Transform dizendo que é a resposta da fonte -- uma
// mentira que ninguém teria como notar.
func TestTransformNaoMutaOQueOExtractEntregou(t *testing.T) {
	original := map[string]any{"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t"}

	saida, pulou, err := applyAll([]Transformer{IngestionID(), IngestionLoadedAt()}, original)
	if err != nil || pulou {
		t.Fatalf("applyAll: %v, pulou=%v", err, pulou)
	}

	if len(original) != 4 {
		t.Errorf("a cadeia escreveu no registro do extract: %v", original)
	}
	obj := saida.(map[string]any)
	if len(obj) != 6 {
		t.Errorf("a saída não tem as duas colunas novas: %v", obj)
	}
}

// TestCadeiaFazUmaCopiaSo é a conta que justifica a mudança.
func TestCadeiaFazUmaCopiaSo(t *testing.T) {
	fns := []Transformer{
		Accept("provider", "entity", "source_key", "record_ts"),
		Rename(map[string]string{"record_ts": "ts"}),
		Compute("extra", func(map[string]any) (any, error) { return 1, nil }),
		Without("extra"),
	}

	alocacoes := testing.AllocsPerRun(200, func() {
		linha := map[string]any{"provider": "p", "entity": "e", "source_key": "k", "record_ts": "t", "lixo": 1}
		if _, _, err := applyAll(fns, linha); err != nil {
			t.Fatal(err)
		}
	})

	// A linha de entrada custa um mapa, a cópia da cadeia custa outro. Quatro
	// transformers que copiassem custariam quatro a mais -- e é essa diferença
	// que o teto pega, não um número absoluto de alocações.
	const teto float64 = 12
	if alocacoes > teto {
		t.Errorf("%.0f alocações para uma linha e quatro transformers (teto %.0f); "+
			"algum transformer voltou a copiar o mapa", alocacoes, teto)
	}
	t.Logf("%.0f alocações", alocacoes)
}
