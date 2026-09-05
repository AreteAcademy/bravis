package from

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Many lê de N origens e entrega tudo como uma sequência só.
//
//	From: from.Many{
//	    Sources: fontes,              // uma por município, por conta, por dia
//	    Workers: 8,
//	    OnError: sdk.ContinueOnError,
//	},
//
// Todo ETL que lê de muitas origens escreve o mesmo laço: itera, tolera a falha
// de algumas, registra quais falharam, acumula. Este é esse laço, escrito uma
// vez.
//
// # A tolerância a falha, e por que ela não é o padrão
//
// Com AbortOnError -- o padrão -- a primeira falha para tudo, que é o que o SDK
// sempre fez. Num fan-out de milhares de origens isso é caro: a leitura 3.000
// derruba as 1.803 que já tinham dado certo, e a próxima execução refaz as
// 3.000.
//
// Com ContinueOnError, a origem que falha é registrada em
// Result.FailedSources e a leitura segue. É a mesma política que o load já tem
// para uma linha ruim -- ele a reporta em ErrorRows e continua --, e a
// assimetria entre os dois lados era o que faltava.
//
// O padrão continua sendo abortar porque mudá-lo em silêncio faria uma execução
// que hoje falha passar a "dar certo" com metade do dado.
//
// # A ordem
//
// Com Workers em 0 ou 1, as origens são lidas em ordem e a sequência é
// determinística -- duas execuções sobre as mesmas origens produzem a mesma
// sequência.
//
// Acima disso, NÃO. Os registros chegam na ordem em que as origens respondem, e
// isso muda entre execuções. Isso não afeta o ingestion_id, que sai dos campos
// do registro e não da posição; afeta o preview, e afeta qualquer coisa que
// dependa de ordem. Concorrência é opt-in por isso.
type Many struct {
	// Sources são as origens. Obrigatório, e ao menos uma.
	Sources []core.Reader

	// Workers é quantas origens são lidas ao mesmo tempo. Zero ou 1 lê em
	// ordem, uma de cada vez.
	//
	// O teto útil depende do que está do outro lado: milhares de requisições
	// ao mesmo host esbarram no pool de conexões do transporte, e o
	// RateLimiter da origem continua valendo por origem.
	Workers int

	// OnError diz o que fazer quando uma origem falha. Zero é AbortOnError.
	OnError core.FailurePolicy
}

// Describe satisfaz core.Reader.
//
// Ele e chamado no caminho de ERRO -- e onde a mensagem de uma configuracao
// invalida vai buscar o nome da origem --, entao ele nao pode estourar com uma
// configuracao invalida. Foi um teste de configuracao invalida que encontrou
// isso: uma origem nil derrubava o processo justamente quando a mensagem mais
// importava.
func (m Many) Describe() string {
	if len(m.Sources) == 0 {
		return "many: (nenhuma origem)"
	}
	if m.Sources[0] == nil {
		return fmt.Sprintf("many: %d origens", len(m.Sources))
	}
	return fmt.Sprintf("many: %d origens, a primeira %s", len(m.Sources), m.Sources[0].Describe())
}

// Read satisfaz core.Reader.
func (m Many) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	if len(m.Sources) == 0 {
		return nil, fmt.Errorf("from.Many precisa de ao menos uma origem em Sources")
	}
	for i, s := range m.Sources {
		if s == nil {
			return nil, fmt.Errorf("from.Many: a origem %d é nil", i)
		}
	}

	trabalhadores := m.Workers
	if trabalhadores < 1 {
		trabalhadores = 1
	}
	if trabalhadores > len(m.Sources) {
		trabalhadores = len(m.Sources)
	}

	inicio := time.Now()
	return func(yield func(core.Envelope, error) bool) {
		ctx, cancelar := context.WithCancel(ctx)
		defer cancelar()

		// Cada origem tem o SEU Stats, e eles são somados no fim. Um ponteiro
		// compartilhado entre goroutines seria corrida de dados -- e o
		// -race a acharia, mas só depois de alguém escrever o teste.
		var mu sync.Mutex
		var falhas []core.SourceFailure
		total := core.Stats{}

		type resultado struct {
			env core.Envelope
			err error
			// origem é preenchido só quando err vem da ABERTURA da fonte,
			// porque é aí que dá para dizer qual falhou.
			origem core.Reader
		}

		fila := make(chan int)
		saida := make(chan resultado)

		var wg sync.WaitGroup
		for w := 0; w < trabalhadores; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range fila {
					fonte := m.Sources[i]

					// Um Stats por origem, somado no fim: assim os contadores
					// do resultado descrevem a leitura inteira e não a última.
					porOrigem := core.Stats{}
					opcoes := opt
					opcoes.Stats = &porOrigem
					// O preview é do conjunto e é montado aqui em cima; pedi-lo
					// a cada origem imprimiria N tabelas.
					opcoes.Preview = 0

					linhas, err := fonte.Read(ctx, opcoes)
					if err != nil {
						select {
						case saida <- resultado{err: err, origem: fonte}:
						case <-ctx.Done():
						}
						continue
					}

					for env, err := range linhas {
						select {
						case saida <- resultado{env: env, err: err, origem: fonte}:
						case <-ctx.Done():
							return
						}
						if err != nil {
							break
						}
					}

					mu.Lock()
					total.Pages += porOrigem.Pages
					total.Attempts += porOrigem.Attempts
					total.Bytes += porOrigem.Bytes
					mu.Unlock()
				}
			}()
		}

		go func() {
			defer close(fila)
			for i := range m.Sources {
				select {
				case fila <- i:
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() { wg.Wait(); close(saida) }()

		linhas := 0
		var amostra []any
		abortou := false

		for r := range saida {
			if r.err != nil {
				if m.OnError != core.ContinueOnError {
					yield(core.Envelope{}, fmt.Errorf("%s: %w", r.origem.Describe(), r.err))
					abortou = true
					cancelar()
					break
				}
				mu.Lock()
				falhas = append(falhas, core.SourceFailure{
					Source: r.origem.Describe(), Err: r.err.Error(),
				})
				mu.Unlock()
				slog.WarnContext(ctx, "origem falhou e foi tolerada",
					"source", r.origem.Describe(), "error", r.err)
				continue
			}

			linhas++
			if opt.Preview > 0 && len(amostra) < opt.Preview {
				amostra = append(amostra, r.env.Payload)
			}
			if !yield(r.env, nil) {
				cancelar()
				break
			}
		}

		// Drena o que estiver em voo, para que nenhuma goroutine fique presa
		// escrevendo num canal que ninguém mais lê.
		cancelar()
		for range saida { //nolint:revive // drenar é o efeito
		}

		if abortou {
			return
		}

		mu.Lock()
		total.FailedSources = falhas
		copiaFalhas := append([]core.SourceFailure(nil), falhas...)
		mu.Unlock()

		if opt.Stats != nil {
			sort.Slice(total.FailedSources, func(i, j int) bool {
				return total.FailedSources[i].Source < total.FailedSources[j].Source
			})
			opt.Stats.Pages += total.Pages
			opt.Stats.Attempts += total.Attempts
			opt.Stats.Bytes += total.Bytes
			opt.Stats.FailedSources = total.FailedSources
		}

		decorrido := time.Since(inicio)
		core.LogExtract(ctx, "many", m.Describe(), core.PreviewStats{
			Rows: linhas, Pages: total.Pages, Bytes: total.Bytes, Duration: decorrido,
		})
		if opt.Preview > 0 {
			core.WritePreview(opt.PreviewWriter, amostra, opt.PreviewBytes, core.PreviewStats{
				Rows: linhas, Pages: total.Pages, Bytes: total.Bytes, Duration: decorrido,
			})
		}

		// Zero registro de N origens boas é um resultado. Zero porque as N
		// falharam é uma execução quebrada, e as duas não podem parecer a
		// mesma coisa para quem lê o log.
		if linhas == 0 && len(copiaFalhas) == len(m.Sources) {
			yield(core.Envelope{}, core.ErrTodasAsFontesFalharam(len(m.Sources), copiaFalhas[0]))
		}
	}, nil
}
