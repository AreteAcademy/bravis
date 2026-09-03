// Package sdk is the front door: two calls to write a fetcher.
//
//	dados, err := sdk.Extract(ctx, sdk.Fonte{
//		URL:      "https://api.open-meteo.com/v1/forecast?...",
//		Guarda:   sdk.RecusarSe("error"),
//		Expandir: sdk.ArraysParalelos("hourly", "time", "temperature_2m"),
//	})
//
//	res, err := sdk.Load(ctx, dados, sdk.Destino{
//		Provider: "open_meteo",
//		Entity:   "hourly_temperature",
//		Chave:    sdk.Chave("latitude", "longitude", "time"),
//		Quando:   sdk.Campo("time"),
//	})
//
// Everything between those two calls that is not specific to the vendor lives
// in here: config, retry, pagination, expansion, provenance, table creation,
// deduplication and the result you log.
//
// The lower-level packages stay available and unchanged. Reach for
// sdk/extract and sdk/load directly when you need a shape these two calls do
// not cover -- the hard case has to stay possible.
package sdk

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"time"

	"github.com/AreteAcademy/bravis/sdk/extract"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
	"github.com/AreteAcademy/bravis/sdk/load"
)

// Dados is a stream of records with the statistics of the fetch that produced
// them. It is an iterator, not a slice: a paginated source must not have to
// fit in memory before the first record can be used.
type Dados struct {
	Registros iter.Seq2[Envelope, error]

	fonte  Fonte
	inicio time.Time
}

// Extract fetches, decodes and, when Fonte.Expandir is set, expands the
// response into one record per reading.
//
// The returned records carry only Payload. Provider, Entity, SourceKey and
// RecordTS are provenance, and provenance is decided at Load, where Destino
// says how to derive it.
func Extract(ctx context.Context, fonte Fonte) (*Dados, error) {
	if fonte.Formato == "" {
		fonte.Formato = FormatoJSON
	}

	if fonte.Guarda != nil && fonte.Guard == nil {
		fonte.Guard = fonte.Guarda
	}

	inicio := time.Now()

	var (
		linhas iter.Seq2[Envelope, error]
		err    error
	)
	switch fonte.Formato {
	case FormatoJSON:
		linhas, err = extract.JSON(ctx, fonte)
	case FormatoNDJSON:
		linhas, err = extract.NDJSON(ctx, fonte)
	case FormatoCSV:
		linhas, err = extract.CSV(ctx, fonte)
	case FormatoXML:
		linhas, err = extract.XML(ctx, fonte)
	default:
		return nil, fmt.Errorf("formato %q desconhecido; use JSON, NDJSON, CSV ou XML", fonte.Formato)
	}
	if err != nil {
		return nil, classificarExtract(fonte, err)
	}

	if fonte.Expandir != nil {
		linhas = expandirStream(fonte, linhas)
	}

	return &Dados{Registros: linhas, fonte: fonte, inicio: inicio}, nil
}

// expandirStream applies the expansor to each decoded document, emitting one
// record per reading. It stays lazy: page N is not held waiting for page N+1.
func expandirStream(fonte Fonte, linhas iter.Seq2[Envelope, error]) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		doc := 0
		for env, err := range linhas {
			if err != nil {
				if !yield(Envelope{}, classificarExtract(fonte, err)) {
					return
				}
				continue
			}

			registros, err := fonte.Expandir(env.Payload)
			if err != nil {
				yield(Envelope{}, &ErroDeFormato{
					URL:     redigir(fonte.URL),
					Formato: string(fonte.Formato),
					Linha:   doc,
					Causa:   err,
				})
				return
			}
			doc++

			for _, r := range registros {
				if !yield(Envelope{Payload: r}, nil) {
					return
				}
			}
		}
	}
}

// Load stamps provenance on every record and writes them to BigQuery.
//
// It resolves configuration with the documented precedence, logs where each
// value came from, creates the landing table when absent, and reports what it
// actually did.
func Load(ctx context.Context, dados *Dados, destino Destino) (*Resultado, error) {
	inicio := time.Now()

	if dados == nil {
		return nil, fmt.Errorf("Load recebeu dados nulos: chame Extract primeiro")
	}

	cfg, origens, err := destino.resolver()
	if err != nil {
		return nil, err
	}
	logResolucao(ctx, origens)

	envelopes, err := coletar(dados, destino)
	if err != nil {
		return nil, err
	}

	res := &Resultado{
		Registros:    int64(len(envelopes)),
		TempoExtract: time.Since(dados.inicio),
		Tabela:       fmt.Sprintf("%s.%s", cfg.Dataset, cfg.Table),
	}

	if len(envelopes) == 0 {
		res.Duracao = time.Since(inicio)
		return res, nil
	}

	inicioLoad := time.Now()

	carregador, err := load.New(ctx, cfg)
	if err != nil {
		return res, &ErroDeDestino{Tabela: res.Tabela, Causa: err}
	}

	lr, err := carregador.Load(ctx, envelopes...)
	res.TempoLoad = time.Since(inicioLoad)
	res.Duracao = time.Since(inicio)

	if lr != nil {
		aplicar(res, lr)
	}
	if err != nil {
		return res, &ErroDeDestino{Tabela: res.Tabela, Linhas: res.ErrosPorLinha, Causa: err}
	}

	return res, nil
}

// coletar drains the stream, stamping provenance from Destino onto each
// record. Load needs the batch in hand to choose a strategy and to size the
// staged file, so this is where streaming ends.
func coletar(dados *Dados, destino Destino) ([]Envelope, error) {
	quando := destino.Quando
	if quando == nil {
		quando = Agora()
	}

	var envelopes []Envelope
	i := 0
	for env, err := range dados.Registros {
		if err != nil {
			return nil, err
		}

		chave, err := destino.Chave(env.Payload)
		if err != nil {
			return nil, &ErroDeFormato{
				URL: redigir(dados.fonte.URL), Formato: string(dados.fonte.Formato),
				Linha: i, Causa: fmt.Errorf("montando source_key: %w", err),
			}
		}

		ts, err := quando(env.Payload)
		if err != nil {
			return nil, &ErroDeFormato{
				URL: redigir(dados.fonte.URL), Formato: string(dados.fonte.Formato),
				Linha: i, Causa: fmt.Errorf("lendo record_ts: %w", err),
			}
		}

		env.Provider = destino.Provider
		env.Entity = destino.Entity
		env.SourceKey = chave
		env.RecordTS = ts

		envelopes = append(envelopes, env)
		i++
	}

	return envelopes, nil
}

func aplicar(res *Resultado, lr *core.LoadResult) {
	res.Linhas = lr.RowsLoaded
	res.Ignoradas = lr.RowsIgnored
	res.Bytes = lr.BytesStaged
	res.Estrategia = lr.Strategy
	res.Formato = lr.Format
	res.Dedup = lr.Dedup
	res.TabelaCriada = lr.TableCreated
	res.ErrosPorLinha = lr.ErrorRows
}

// classificarExtract turns a transport or decode failure into the typed error
// that says which action it calls for.
func classificarExtract(fonte Fonte, err error) error {
	url := redigir(fonte.URL)

	tentativas := 1
	if fonte.RetryConfig != nil {
		tentativas = fonte.RetryConfig.MaxAttempts
	}

	if status, ok := statusDe(err); ok {
		return &ErroDeFonte{URL: url, Status: status, Tentativas: tentativas, Causa: err}
	}
	if ehDeTransporte(err) {
		return &ErroDeFonte{URL: url, Tentativas: tentativas, Causa: err}
	}
	return &ErroDeFormato{URL: url, Formato: string(fonte.Formato), Linha: -1, Causa: err}
}

var _ = slog.LevelInfo
