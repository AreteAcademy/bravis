package from_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// fonteFalsa entrega N registros, ou falha.
type fonteFalsa struct {
	nome      string
	linhas    int
	erroAbrir error
	erroApos  int // > 0: falha depois de N linhas
}

func (f fonteFalsa) Describe() string { return f.nome }

func (f fonteFalsa) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	if f.erroAbrir != nil {
		return nil, f.erroAbrir
	}
	return func(yield func(sdk.Envelope, error) bool) {
		for i := 0; i < f.linhas; i++ {
			if f.erroApos > 0 && i == f.erroApos {
				yield(sdk.Envelope{}, fmt.Errorf("%s quebrou na linha %d", f.nome, i))
				return
			}
			if !yield(sdk.Envelope{Payload: map[string]any{"origem": f.nome, "i": i}}, nil) {
				return
			}
		}
	}, nil
}

func drenar(t *testing.T, fonte sdk.Reader, stats *sdk.Stats) ([]map[string]any, error) {
	t.Helper()
	dados, err := sdk.Extract(context.Background(), sdk.Source{From: fonte, Stats: stats})
	if err != nil {
		return nil, err
	}
	var linhas []map[string]any
	for env, err := range dados.Records {
		if err != nil {
			return linhas, err
		}
		linhas = append(linhas, env.Payload.(map[string]any))
	}
	return linhas, nil
}

// TestManyJuntaAsOrigens: o caso base.
func TestManyJuntaAsOrigens(t *testing.T) {
	linhas, err := drenar(t, from.Many{Sources: []sdk.Reader{
		fonteFalsa{nome: "a", linhas: 2},
		fonteFalsa{nome: "b", linhas: 3},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(linhas) != 5 {
		t.Errorf("%d linhas, esperado 5", len(linhas))
	}
}

// TestManySequencialMantemAOrdem: com Workers 0 ou 1 a sequência é
// determinística, e é por isso que esse é o padrão. Concorrência é opt-in
// justamente porque ela abre mão disso.
func TestManySequencialMantemAOrdem(t *testing.T) {
	fontes := []sdk.Reader{
		fonteFalsa{nome: "a", linhas: 2},
		fonteFalsa{nome: "b", linhas: 2},
		fonteFalsa{nome: "c", linhas: 2},
	}
	var anterior string
	for tentativa := 0; tentativa < 20; tentativa++ {
		linhas, err := drenar(t, from.Many{Sources: fontes}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var ordem []string
		for _, l := range linhas {
			ordem = append(ordem, fmt.Sprintf("%s%v", l["origem"], l["i"]))
		}
		atual := strings.Join(ordem, ",")
		if atual != "a0,a1,b0,b1,c0,c1" {
			t.Fatalf("ordem = %s", atual)
		}
		if tentativa > 0 && atual != anterior {
			t.Fatalf("a ordem variou entre execuções: %s e %s", anterior, atual)
		}
		anterior = atual
	}
}

// TestManyAbortaPorPadrao: mudar o padrão em silêncio faria uma execução que
// hoje falha passar a "dar certo" com metade do dado.
func TestManyAbortaPorPadrao(t *testing.T) {
	_, err := drenar(t, from.Many{Sources: []sdk.Reader{
		fonteFalsa{nome: "boa", linhas: 2},
		fonteFalsa{nome: "ruim", erroAbrir: fmt.Errorf("504")},
		fonteFalsa{nome: "outra", linhas: 2},
	}}, nil)
	if err == nil {
		t.Fatal("o padrão tolerou uma falha")
	}
	if !strings.Contains(err.Error(), "ruim") {
		t.Errorf("o erro não nomeia a origem: %v", err)
	}
}

// TestManyContinuaEDizQuaisFalharam é o item 2 inteiro em um teste.
//
// Num fan-out de milhares de origens, "a execução falhou" não é informação: o
// que resolve é saber QUAIS falharam, para reprocessar essas e não as outras.
func TestManyContinuaEDizQuaisFalharam(t *testing.T) {
	var stats sdk.Stats
	linhas, err := drenar(t, from.Many{
		Sources: []sdk.Reader{
			fonteFalsa{nome: "a", linhas: 2},
			fonteFalsa{nome: "quebrada", erroAbrir: fmt.Errorf("504 do fornecedor")},
			fonteFalsa{nome: "c", linhas: 3},
		},
		OnError: sdk.ContinueOnError,
	}, &stats)
	if err != nil {
		t.Fatalf("ContinueOnError abortou: %v", err)
	}
	if len(linhas) != 5 {
		t.Errorf("%d linhas, esperado 5 -- as boas têm de sobreviver à ruim", len(linhas))
	}
	if len(stats.FailedSources) != 1 {
		t.Fatalf("falhas = %v, esperado uma", stats.FailedSources)
	}
	f := stats.FailedSources[0]
	if f.Source != "quebrada" || !strings.Contains(f.Err, "504") {
		t.Errorf("a falha não diz qual nem por quê: %+v", f)
	}
}

// TestManyFalhaNoMeioDaOrigemTambemEContada: uma origem que quebra na linha 3
// falhou tanto quanto uma que nem abriu.
func TestManyFalhaNoMeioDaOrigemTambemEContada(t *testing.T) {
	var stats sdk.Stats
	linhas, err := drenar(t, from.Many{
		Sources: []sdk.Reader{
			fonteFalsa{nome: "meia", linhas: 10, erroApos: 3},
			fonteFalsa{nome: "inteira", linhas: 2},
		},
		OnError: sdk.ContinueOnError,
	}, &stats)
	if err != nil {
		t.Fatal(err)
	}
	// As 3 que a origem entregou antes de quebrar contam: elas foram lidas.
	if len(linhas) != 5 {
		t.Errorf("%d linhas, esperado 5 (3 da meia + 2 da inteira)", len(linhas))
	}
	if len(stats.FailedSources) != 1 || stats.FailedSources[0].Source != "meia" {
		t.Errorf("falhas = %v", stats.FailedSources)
	}
}

// TestManyTodasFalharemNaoEZeroLinhas: zero registro de N origens boas é um
// resultado; zero porque as N falharam é uma execução quebrada, e as duas não
// podem parecer a mesma coisa.
func TestManyTodasFalharemNaoEZeroLinhas(t *testing.T) {
	_, err := drenar(t, from.Many{
		Sources: []sdk.Reader{
			fonteFalsa{nome: "a", erroAbrir: fmt.Errorf("504")},
			fonteFalsa{nome: "b", erroAbrir: fmt.Errorf("503")},
		},
		OnError: sdk.ContinueOnError,
	}, nil)
	if err == nil {
		t.Fatal("todas as origens falharam e a execução deu certo")
	}
	if !strings.Contains(err.Error(), "as 2 origens falharam") {
		t.Errorf("o erro não diz que foram todas: %v", err)
	}
}

// TestManyConcorrenteLeTudo: com concorrência a ordem muda, o conjunto não.
func TestManyConcorrenteLeTudo(t *testing.T) {
	var fontes []sdk.Reader
	for i := 0; i < 50; i++ {
		fontes = append(fontes, fonteFalsa{nome: fmt.Sprintf("f%02d", i), linhas: 4})
	}

	linhas, err := drenar(t, from.Many{Sources: fontes, Workers: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(linhas) != 200 {
		t.Fatalf("%d linhas, esperado 200", len(linhas))
	}
	vistas := map[string]int{}
	for _, l := range linhas {
		vistas[l["origem"].(string)]++
	}
	if len(vistas) != 50 {
		t.Errorf("%d origens apareceram, esperado 50", len(vistas))
	}
	for nome, n := range vistas {
		if n != 4 {
			t.Errorf("%s entregou %d linhas", nome, n)
		}
	}
}

// TestManySomaOsContadores: os contadores do resultado têm de descrever a
// leitura inteira, e não a última origem.
func TestManySomaOsContadores(t *testing.T) {
	var stats sdk.Stats
	if _, err := drenar(t, from.Many{
		Sources: []sdk.Reader{contadora{2}, contadora{3}, contadora{5}},
		Workers: 3,
	}, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Pages != 10 {
		t.Errorf("Pages = %d, esperado 10 (2+3+5)", stats.Pages)
	}
}

// contadora preenche o Stats que recebe, como um driver de verdade.
type contadora struct{ paginas int }

func (c contadora) Describe() string { return fmt.Sprintf("contadora(%d)", c.paginas) }
func (c contadora) Read(_ context.Context, opt sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		if opt.Stats != nil {
			opt.Stats.Pages = c.paginas
		}
		yield(sdk.Envelope{Payload: map[string]any{"n": c.paginas}}, nil)
	}, nil
}

// TestManyParaDeLerQuandoOConsumidorPara: um break no laço do consumidor não
// pode deixar goroutine presa escrevendo num canal que ninguém lê.
func TestManyParaDeLerQuandoOConsumidorPara(t *testing.T) {
	var fontes []sdk.Reader
	for i := 0; i < 20; i++ {
		fontes = append(fontes, fonteFalsa{nome: fmt.Sprintf("f%d", i), linhas: 1000})
	}

	dados, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Many{Sources: fontes, Workers: 4},
	})
	if err != nil {
		t.Fatal(err)
	}

	feito := make(chan struct{})
	go func() {
		defer close(feito)
		n := 0
		for _, err := range dados.Records {
			if err != nil {
				t.Error(err)
				return
			}
			n++
			if n == 10 {
				break
			}
		}
	}()

	select {
	case <-feito:
	case <-timeout():
		t.Fatal("a iteração não terminou depois do break; alguma goroutine ficou presa")
	}
}

// TestManyRecusaConfiguracaoInvalida.
func TestManyRecusaConfiguracaoInvalida(t *testing.T) {
	if _, err := drenar(t, from.Many{}, nil); err == nil {
		t.Error("aceitou zero origens")
	}
	if _, err := drenar(t, from.Many{Sources: []sdk.Reader{nil}}, nil); err == nil {
		t.Error("aceitou uma origem nil")
	}
}

func timeout() <-chan struct{} {
	c := make(chan struct{})
	go func() { time.Sleep(10 * time.Second); close(c) }()
	return c
}

// TestDiscoverMontaAsOrigensDentroDoPipeline é a segunda metade do item 9.
//
// A lista às vezes só se conhece na execução. Montada antes do sdk.Run, ela
// fica fora do pipeline: sem retry, sem timeout, sem log, e sem aparecer no
// Result quando falha.
func TestDiscoverMontaAsOrigensDentroDoPipeline(t *testing.T) {
	linhas, err := drenar(t, from.Many{
		Discover: func(context.Context) ([]sdk.Reader, error) {
			return []sdk.Reader{
				fonteFalsa{nome: "descoberta-a", linhas: 2},
				fonteFalsa{nome: "descoberta-b", linhas: 3},
			}, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(linhas) != 5 {
		t.Errorf("%d linhas, esperado 5", len(linhas))
	}
}

// TestDiscoverQueFalhaEErroDoExtract: o erro dela é tratado como qualquer
// outro do extract, e não como um panic num main antes de tudo começar.
func TestDiscoverQueFalhaEErroDoExtract(t *testing.T) {
	_, err := drenar(t, from.Many{
		Discover: func(context.Context) ([]sdk.Reader, error) {
			return nil, fmt.Errorf("o endpoint que lista as partições devolveu 503")
		},
	}, nil)
	if err == nil {
		t.Fatal("a descoberta falhou e a execução seguiu")
	}
	if !strings.Contains(err.Error(), "descobrindo as origens") {
		t.Errorf("o erro não diz o que falhou: %v", err)
	}
}

// TestDiscoverVazioNaoEZeroRegistros: uma execução que não leu nada porque não
// havia o que ler é diferente de uma que não sabia onde ler.
func TestDiscoverVazioNaoEZeroRegistros(t *testing.T) {
	_, err := drenar(t, from.Many{
		Discover: func(context.Context) ([]sdk.Reader, error) { return nil, nil },
	}, nil)
	if err == nil {
		t.Fatal("zero origens passou como zero registros")
	}
	if !strings.Contains(err.Error(), "não devolveu origem nenhuma") {
		t.Errorf("erro = %v", err)
	}
}

// TestDiscoverESourcesJuntosERecusado: duas listas de origens, e a que perde
// perderia em silêncio.
func TestDiscoverESourcesJuntosERecusado(t *testing.T) {
	_, err := drenar(t, from.Many{
		Sources:  []sdk.Reader{fonteFalsa{nome: "a", linhas: 1}},
		Discover: func(context.Context) ([]sdk.Reader, error) { return nil, nil },
	}, nil)
	if err == nil {
		t.Fatal("declarar os dois passou")
	}
}
