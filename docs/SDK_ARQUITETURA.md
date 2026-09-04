# SDK — a arquitetura como ela é

**Vale para** `sdk/v0.21.0` · **Atualizado em** 2026-09-04

Este é o mapa. Para *por que* cada peça é assim, veja
[`SDK_DECISOES.md`](SDK_DECISOES.md); para *como acrescentar um driver*, veja
[`SDK_NOVO_DRIVER.md`](SDK_NOVO_DRIVER.md).

---

## 1. As quatro perguntas

Um fetcher responde quatro coisas, e cada uma tem um lugar:

```go
sdk.Run(sdk.Pipeline{
    // 1. De onde vem, e o que uma resposta significa.
    Source: sdk.Source{
        From: from.HTTP{
            URL:     "https://api.open-meteo.com/v1/forecast?...",
            Timeout: 15 * time.Second,
            Records: func(r sdk.Response) ([]any, error) { ... },
        },
        Preview: 5,
    },

    // 2. Que linha monta.
    Transform: []sdk.Transformer{
        sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
        sdk.Rename(map[string]string{"temperature_2m": "temperature_celsius"}),
        sdk.Compute("provider", ...), sdk.Compute("entity", ...),
        sdk.Compute("source_key", ...),
        sdk.IngestionID("provider", "entity", "source_key", "time"),
        sdk.IngestionLoadedAt(),
    },

    // 3. Para onde vai, 4. com que colunas.
    Target: sdk.Target{
        To:      bigquery.Table{Dataset: "bronze", Table: "hourly_temperatures"},
        Columns: []string{"ingestion_id", "ingestion_loaded_at", "time", "temperature_celsius"},
        Dedup: sdk.DedupMerge,
    },
})
```

A mesma coisa em duas chamadas, que é o que os testes usam:

```go
data, err := sdk.Extract(ctx, source)
data = sdk.Transform(data, fns...)
res, err := sdk.Load(ctx, data, target)
```

---

## 2. O mapa dos pacotes

```
sdk/
├── *.go                  a fachada: Pipeline, Run, Extract, Load, Transform,
│                         Source, Target, Metadata, Result, RunContext
├── internal/core/        o contrato: Envelope, Reader, Writer, ReadOptions,
│                         WriteOptions, Response, Reading, Stats, Dedup,
│                         LoadConfig, Origin, Reject
├── from/                 as origens: HTTP, Files  (Postgres e MySQL a caminho)
├── to/                   os destinos sem dependência: Files
├── to/bigquery/          o destino BigQuery, em pacote próprio
├── store/                os backends de object storage: s3, gcs
├── extract/              a implementação HTTP: retry, paginação, decoders,
│                         preview
└── load/                 a implementação BigQuery: staging, MERGE, criação
                          tipada de tabela
```

**A raiz não importa `from`, `to`, `extract` nem `load`.** Essa é a regra que
sustenta tudo o mais, e ela é medida — veja §6.

| quem depende de quem | |
|---|---|
| `sdk` → `internal/core` | só |
| `from/*` → `internal/core`, `extract` | |
| `to/*` → `internal/core`, `load` | |
| `store/*` → nada do SDK | só o cliente da nuvem |
| `extract`, `load` → `internal/core` | |

Nenhuma seta aponta de volta para `sdk`. Um driver não consegue importar a
fachada, e é por isso que `RunContext` e a resolução de configuração moram no
`core`.

---

## 3. As duas interfaces

```go
type Reader interface {
    Read(ctx context.Context, opt ReadOptions) (iter.Seq2[Envelope, error], error)
    Describe() string
}

type Writer interface {
    Write(ctx context.Context, records []Envelope, opt WriteOptions) (*LoadResult, error)
    Describe() string
}
```

`Describe()` é o que aparece no log e na mensagem de erro — `"bronze.pedidos"`,
`"http://api.example.com/v1/events"` —, já sem segredo de query string.

### O que atravessa todos os drivers

| em `ReadOptions` | |
|---|---|
| `Preview`, `PreviewBytes`, `PreviewWriter` | a tabela estilo `head()` |
| `Stats` | páginas, tentativas, bytes lidos |
| `Run` | o contexto de execução do engine |

| em `WriteOptions` | |
|---|---|
| `Columns` | a declaração do destino |

| `Dedup` | a deduplicação pedida |
| `Run` | o contexto de execução do engine |

### O que é do driver

Tudo o mais. `from.HTTP` tem URL, headers, retry, `RateLimiter`, paginação,
`Format` e `Records`; `from.Files` tem caminho, formato e `Store`.
`bigquery.Table` tem projeto, dataset, tabela, staging em GCS, `ClusterBy`,
particionamento e `CreateSQL`; `to.Files` tem caminho, `PartitionBy` e
`Compress`. **Nenhum desses campos aparece
num driver que não os tem** — é essa a diferença entre um tipo por driver e uma
struct de união.

---

## 4. Por onde um registro passa

```
from.X.Read()                     iter.Seq2[Envelope, error]  ← preguiçoso
   │
   ├─ Records / decoder           decide o que a resposta carrega
   │
   ▼
Transform (por registro)          Accept, Rename, Compute, IngestionID,
   │                              IngestionLoadedAt
   │                              SkipRecord descarta um; erro derruba a execução
   ▼
collect (fachada)                 carimba proveniência quando há Metadata:
   │                              Provider, Entity, SourceKey, RecordTS
   ▼
to.X.Write()
   ├─ checkColumns                a linha contra a declaração, nos dois sentidos
   ├─ prepara o destino           cria tipado quando pedido
   ├─ checkDeclaredAgainstTable   a declaração contra o destino real
   └─ escreve                     inline / GCS / MERGE
```

Duas coisas que a ordem explica:

1. **A linha que chega ao destino é exatamente a que a cadeia compôs.** Nada é
   carimbado depois, então a conferência contra `Columns` não tem caso especial.
2. **`IngestionID` lê o registro na posição em que está.** Um `Rename` antes
   obriga a nomear o campo novo, e nomear o antigo é erro listando o que a linha
   de fato tem.

---

## 5. O que o SDK escreve

**As colunas que você compôs no `Transform`, e nada mais.** As duas que o SDK
sabe escrever são transformers, postos na cadeia como qualquer outro:

```sql
ingestion_id        STRING    NOT NULL,
ingestion_loaded_at TIMESTAMP NOT NULL
```

```go
sdk.IngestionID("provider", "entity", "source_key", "time"),
sdk.IngestionLoadedAt(),
```

`sdk.IngestionID` lê os quatro componentes de **campos do registro**. Eles
constroem o id e não viram coluna por si — quem quiser `provider` e `entity` na
tabela os compõe com `Compute`, como qualquer outra coluna.

O `NOT NULL` sai quando `Target.Columns` nomeia a coluna: declare, e o SDK cria
a tabela para poder apertá-la; não declare, e tudo é inferido nullable.

---

## 6. A regra que sustenta a arquitetura

**Go poda dependência por pacote importado, nunca por campo usado.** A única
forma de um consumidor não pagar por um driver é não importar o pacote dele.

Medido na `v0.19.0`:

| o que se importa | pacotes | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | não | não |
| `sdk` + `from` | 194 | não | não |
| `sdk` + `from` + `to` (arquivos) | 195 | não | não |
| `sdk` + `to/bigquery` | 456 | não | sim |
| `sdk` + `from` + `store/s3` | 265 | sim | não |
| `sdk` + `from` + `store/gcs` | 392 | não | sim |

Um fetcher HTTP de verdade — com o `main`, `fmt` e o resto — sai em **197
pacotes e 9,1 MB**, medido contra a `v0.19.0` publicada.

Antes da fase 0 eram **458 pacotes e 21 MB** para qualquer consumidor, porque a
raiz importava `sdk/load` e ele importa BigQuery, Arrow e Thrift.

Há teste afirmando isso em `examples/consumer/pruning_test.go`, **com o
controle junto**: quem importa `to` tem de receber o BigQuery, senão o teste
passaria com um SDK que não carrega nada.

**A regra, em uma linha: um driver com SDK de fornecedor atrás mora no próprio
pacote.** Por isso o BigQuery é `to/bigquery` e os object stores são
`store/s3` e `store/gcs`, enquanto `from` e `to` guardam os que só precisam da
biblioteca padrão.

Isso foi aprendido caro na `v0.20.0`: `to.BigQuery` e `to.Files` saíram no
mesmo pacote, e escrever um arquivo compilava o Google — 461 pacotes e 21 MB
onde deviam ser 195. O teste de poda não pegou porque só cobria o lado `from`.

> Ao acrescentar um driver: se a raiz passar a importá-lo, ou se ele dividir
> pacote com um driver caro, a propriedade morre em silêncio. Cubra o caso no
> teste de poda — **inclusive o pipeline completo, dos dois lados.**

---

## 7. Erros

| tipo | quer dizer | o que fazer |
|---|---|---|
| `SourceError` | a origem falhou no transporte | tentar de novo depois |
| `FormatError` | o dado veio, com forma errada | consertar o mapeamento |
| `TargetError` | o destino recusou | ler `RowErrors` |
| `Rejection` (`sdk.Reject`) | a fonte mandou algo que não é dado | reexecutar a mesma janela dá no mesmo |

`errors.Is(err, sdk.ErrRejected)` separa recusa de erro de programação — os
dois derrubam a execução, mas pedem coisas diferentes de quem está de plantão.

`sdk.SkipRecord`, devolvido de um `Transformer`, descarta **aquele registro**
sem falhar a execução.

---

## 8. Observabilidade

- `Result.Args()` é a linha de log inteira de um fetcher.
- `Source.Preview` imprime os primeiros N registros como tabela, num writer, ao
  fim do extract — inclusive quando a fonte morre no meio, que é quando mais se
  quer ver o que chegou.
- `-dry-run` extrai, transforma e imprime sem escrever. `-preview N` liga a
  tabela sem recompilar. `-v` sobe para debug.
- `RunContext` diz o que o engine sabe da execução: `First`, `Attempt`,
  `Trigger`, `LogicalDate`, `Params`. Fora do engine vem zerado, e ignorá-lo
  não custa nada.

---

## 9. Onde as coisas estão

| você quer | está em |
|---|---|
| a fachada, `Pipeline`, `Run` | `sdk/pipeline.go`, `sdk/sdk.go` |
| `Source` e `Target` | `sdk/source.go`, `sdk/target.go` |
| as interfaces | `sdk/internal/core/driver.go` |
| `Transform` e compositores | `sdk/transform.go`, `sdk/expand.go` |
| o `ingestion_id` | `sdk/internal/core/types.go` (`IngestionID`) |
| a reconciliação de colunas | `sdk/load/columns.go`, `sdk/load/merge_sql.go` |
| o preview | `sdk/internal/core/preview.go` |
| os transformers de ingestão | `sdk/ingestion.go` |
| a fórmula congelada do id | `sdk/internal/core/types.go` |
| o `CheckColumns` | `sdk/internal/core/metadata.go` |
| os backends de nuvem | `sdk/store/s3`, `sdk/store/gcs` |
| os testes contra o BigQuery real | `sdk/load/integration_test.go` |
| os testes contra MinIO e GCS | `sdk/from/integration_test.go` |
| as provas de consumidor | `examples/consumer/` |
