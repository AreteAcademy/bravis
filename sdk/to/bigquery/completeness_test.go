package bigquery

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// bigquery.Table é um adaptador: ele traduz os seus campos para a LoadConfig
// que o pacote load consome. Um campo do LoadConfig que ele nunca escreve é um
// ajuste que o consumidor da fachada não tem como fazer -- e nada quebra,
// nada avisa.
//
// Aconteceu de verdade: a fase 0 parou de repassar Provider e Entity, e toda
// tabela criada desde então saiu sem os labels de atribuição de custo. Nenhum
// teste viu, porque contagem de linha nenhuma muda.
//
// Este teste lê os dois arquivos e compara. É grosseiro de propósito: ele
// falha quando alguém acrescenta um campo ao LoadConfig e esquece de ligá-lo
// aqui, que é exatamente quando se quer ser incomodado.
func TestTodoCampoDoLoadConfigEAlcancavel(t *testing.T) {
	campos := camposDe(t, "../../internal/core/types.go", "LoadConfig")
	escritos := escritosPor(t, "bigquery.go")

	// Estes o adaptador não escreve, e por razões declaradas.
	naoSeAplica := map[string]string{
		"Format":   "sempre ndjson: é o único formato que o load escreve",
		"Provider": "vem do lote, não do Target -- ver Write",
		"Entity":   "vem do lote, não do Target -- ver Write",
	}

	var faltando []string
	for _, c := range campos {
		if escritos[c] {
			continue
		}
		if _, ok := naoSeAplica[c]; ok {
			continue
		}
		faltando = append(faltando, c)
	}

	if len(faltando) > 0 {
		sort.Strings(faltando)
		t.Errorf("LoadConfig tem %s, e bigquery.Table não os define nem os declara "+
			"inaplicáveis. Quem usa a fachada não consegue ajustá-los, e nada avisa.",
			strings.Join(faltando, ", "))
	}
}

var (
	campoRe   = regexp.MustCompile(`(?m)^\t([A-Z][A-Za-z0-9]*)\s`)
	escritaRe = regexp.MustCompile(`(?m)^\t+([A-Z][A-Za-z0-9]*):`)
	atribRe   = regexp.MustCompile(`cfg\.([A-Z][A-Za-z0-9]*)\s*(,|=)`)
)

func camposDe(t *testing.T, caminho, tipo string) []string {
	t.Helper()
	data, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("lendo %s: %v", caminho, err)
	}
	s := string(data)
	i := strings.Index(s, "type "+tipo+" struct {")
	if i < 0 {
		t.Fatalf("%s não achado em %s", tipo, caminho)
	}
	corpo := s[i:]
	corpo = corpo[:strings.Index(corpo, "\n}")]

	var out []string
	for _, m := range campoRe.FindAllStringSubmatch(corpo, -1) {
		out = append(out, m[1])
	}
	return out
}

func escritosPor(t *testing.T, caminho string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("lendo %s: %v", caminho, err)
	}
	s := string(data)

	out := map[string]bool{}
	for _, m := range escritaRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	for _, m := range atribRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}
