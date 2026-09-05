# `sdk.load` — o consumidor declara as colunas, o SDK não inventa nenhuma

**Escrito em** 2026-09-03 · **Base** `sdk/v0.13.0` · **Alvo** `sdk/v0.14.0`
(quebra compatibilidade, de propósito)

Pedido de quem consome o SDK, na íntegra:

> O sdk.load precisa deixar clara a estrutura da tabela, sem ocultar nada. O
> consumidor precisa decidir como devem ser as colunas.

Isto não é um pedido de conforto. É a causa raiz dos três incidentes que este
consumidor teve em três dias, e a spec abaixo existe para fechá-la de vez.

---

## 0. O inventário do que está oculto hoje

Cinco lugares em que uma coluna passa a existir sem que o fetcher a mencione:

| # | o quê | onde |
|---|---|---|
| 1 | `ExtraMetadata: true` acrescenta `ingestion_id` e `ingestion_loaded_at` | `load.go:299-303` |
| 2 | `CreateTable` sem `CreateSQL` infere o schema do payload | `table.go:102`, `file.AutoDetect = true` |
| 3 | A partição é escolhida pelo SDK, em `ingestion_loaded_at`, quando `ExtraMetadata` está ligado | `table.go:106-108` |
| 4 | Na `v0.8.0`, `WriteEnvelopeColumns: !RawPayload` promovia `Provider` e `Entity` a colunas, aninhava o payload sob `payload` e derivava `source_key` | `v0.8.0 target.go:127` |
| 5 | `CreateSQL` é uma string de DDL conferida só por "a tabela apareceu?" | `table.go:70-77` |

E uma sexta, que é ausência e não excesso: **`reconcile` só roda no caminho do
`DedupMerge`** (`dedup.go:74`). Um fetcher sem dedup não tem validação de schema
nenhuma — quem reclama é o BigQuery, com a mensagem dele.

### O custo, medido

O item 4 era um **default**. O `main.go` do consumidor declara `Provider` e
`Entity` como **procedência**, para compor o `ingestion_id`; que o SDK também os
promovesse a colunas era invisível dali.

Quando a `v0.9.0` parou de preencher três das seis colunas, **nada do lado do
consumidor acusou**. A tabela continuou existindo, com as colunas lá, vazias. O
sintoma chegou dias depois como um erro de tipo do BigQuery
(`FLOAT64 ... into column ingestion_id`), e a causa levou três versões para ser
isolada.

O teste decisivo é uma pergunta que se pode fazer a qualquer fetcher:

> Onde estão declaradas as colunas `provider`, `entity` e `payload`?

Hoje a resposta honesta é "em nenhum código". É isso que precisa mudar.

---

> **FECHADA.** I1 na `sdk/v0.18.0`, I5 na `v0.24.0`, e I2, I3 e I4 na
> `v0.35.0`. Dois deles saíram por um caminho diferente do que esta spec
> propõe — a §14 do [`SDK_DECISOES.md`](../SDK_DECISOES.md) registra qual e por
> quê.

## 1. Os invariantes

Depois desta mudança, tudo abaixo tem de valer:

- **I1.** Nenhuma coluna existe no destino sem estar escrita no fetcher.
- **I2.** O SDK nunca infere schema. Autodetect deixa de ser caminho de criação.
- **I3.** Divergência entre o que o fetcher declara e a tabela real é erro
  nomeando a coluna e a diferença — e acontece **antes do extract**, não depois.
- **I4.** Partição e clusterização são declarados. O SDK não escolhe layout.
- **I5.** Como cada coluna é preenchida é declarado: de um campo do registro, de
  procedência, ou do registro inteiro.

I5 é o que falta hoje mesmo com `CreateSQL`: dá para declarar o schema e ainda
assim não saber o que cai em cada coluna.

---

## 2. A forma proposta

Um `Schema` no `Target`, com a origem de cada coluna:

```go
Target: sdk.Target{
    Driver:   sdk.DriverBigQuery,
    Provider: "open_meteo",
    Entity:   "hourly_temperature",
    Dataset:  "bronze",
    Table:    "vendors_open_meteo_hourly_temperatures",
    Key:      sdk.Key("latitude", "longitude", "time"),
    When:     sdk.Field("time"),
    Dedup:    sdk.DedupMerge,

    Schema: sdk.Schema{
        {Name: "ingestion_id",        Type: sdk.String,    Required: true, From: sdk.IngestionID},
        {Name: "ingestion_loaded_at", Type: sdk.Timestamp, Required: true, From: sdk.LoadedAt},
        {Name: "provider",            Type: sdk.String,    Required: true, From: sdk.ProviderOf},
        {Name: "entity",              Type: sdk.String,    Required: true, From: sdk.EntityOf},
        {Name: "source_key",          Type: sdk.String,                    From: sdk.SourceKeyOf},
        {Name: "payload",             Type: sdk.JSON,      Required: true, From: sdk.WholeRecord},
    },
    PartitionBy: "ingestion_loaded_at",
    ClusterBy:   []string{"provider", "entity"},
},
```

E uma tabela plana é a **mesma** declaração, com outras origens:

```go
    Schema: sdk.Schema{
        {Name: "ingestion_id",        Type: sdk.String,    Required: true, From: sdk.IngestionID},
        {Name: "ingestion_loaded_at", Type: sdk.Timestamp, Required: true, From: sdk.LoadedAt},
        {Name: "time",                Type: sdk.String,    Required: true, From: sdk.FieldOf("time")},
        {Name: "temperature_2m",      Type: sdk.Float64,                   From: sdk.FieldOf("temperature_2m")},
        {Name: "latitude",            Type: sdk.Float64,                   From: sdk.FieldOf("latitude")},
        {Name: "longitude",           Type: sdk.Float64,                   From: sdk.FieldOf("longitude")},
    },
    PartitionBy: "ingestion_loaded_at",
```

### Por que isto encerra a briga do contrato

O item 4 do [`SDK_V9.md`](../SDK_V9.md) — "quem produz as seis colunas" — **deixa
de existir como pergunta**. O SDK não produz nenhuma e não recusa nenhuma: as
duas formas acima são declarações, e a escolha é de quem escreve o fetcher.

Essa pergunta já mudou de resposta três vezes (agnóstico na `v0.1.1`, contrato
na `v0.2.1`, agnóstico de novo na `v0.9.0`). Enquanto for o SDK que decide, ela
vai mudar de novo. Com `Schema`, não há o que decidir.

### O que `From` precisa cobrir

| origem | o que escreve |
|---|---|
| `sdk.FieldOf("x")` | o campo `x` do registro, depois do `Transform` |
| `sdk.WholeRecord` | o registro inteiro como JSON |
| `sdk.IngestionID` | o UUID v5 que o SDK já calcula |
| `sdk.LoadedAt` | o instante da carga |
| `sdk.ProviderOf` / `sdk.EntityOf` / `sdk.SourceKeyOf` | a procedência declarada no `Target` |
| `sdk.Const(v)` | um literal |

Um campo do registro que **nenhuma** coluna consome é erro nomeando o campo —
mesma assimetria que a `reconcile` já usa hoje, e pelo mesmo motivo: descartar
dado em silêncio é o pior modo de falhar. A saída para quem quis mesmo descartar
é o `Without` no `Transform`, que diz isso em voz alta.

---

## 3. O que acontece com o que existe

| hoje | depois |
|---|---|
| `ExtraMetadata bool` | **sai.** Quem quer as duas colunas declara as duas colunas. Um `bool` que acrescenta campos com nome fixo é exatamente o ocultamento que esta spec fecha |
| `CreateTable` + autodetect | **sai o autodetect.** `CreateTable` passa a criar a partir do `Schema` |
| `CreateSQL` | **fica**, para quem tem DDL que o `Schema` não expressa (views, opções exóticas). Mas passa a ser conferido contra o `Schema`, não só contra "a tabela apareceu" |
| `ClusterBy` | fica como está: já é declarado |
| partição implícita | **sai.** Vira `PartitionBy`, um nome de coluna que tem de estar no `Schema` |
| `reconcile` no dedup | **generaliza**: passa a rodar nos três caminhos, contra o `Schema` |

**`Schema` é obrigatório.** Não deixe fallback para inferência: um campo
opcional que, ausente, restaura o comportamento antigo mantém os cinco
ocultamentos vivos e some da revisão. É uma quebra, e ela merece um major visível
no `CHANGELOG` — o consumidor é um só, e prefere a quebra ao silêncio.

---

## 4. Validar antes do extract, não depois

Hoje a conferência acontece no `Load`, com o extract inteiro já feito. Num vendor
com cota ou cobrança por chamada, é quota gasta para descobrir que a coluna não
bate.

Com `Schema` declarado, a conferência contra a tabela real não depende de dado
nenhum: são dois schemas. Faça-a **no começo do `Run`**, antes do `Extract`.

O consumidor deste SDK declara as landings em
`dbt/macros/config/setup_vendors.sql` e **não** deixa o SDK criá-las. Então o
modo normal é "a tabela já existe, confira contra ela" — e é esse o caminho que
precisa ficar mais bem coberto, não o de criação.

---

## 5. O que não fazer

- **Não** deixe `Schema` opcional com inferência de reserva. Ver §3.
- **Não** altere tabela existente. O princípio de `table.go:26` continua valendo:
  um loader que sabe fazer `ALTER` sabe apagar história. Divergência é erro, não
  migração.
- **Não** reintroduza uma forma de linha padrão, nem plana nem envelope. A lição
  das três reviravoltas é que o SDK não deve ter opinião sobre isso.
- **Não** faça o `Schema` inferir tipo do Go. `float64` do `encoding/json` viraria
  `FLOAT64` numa coluna que o consumidor queria `NUMERIC`, e a inferência estaria
  de volta pela porta dos fundos.

---

## 6. Critério de pronto para a `sdk/v0.14.0`

1. `Schema` obrigatório no `Target`; `New` recusa sem ele, nomeando o campo.
2. Toda coluna escrita vem de uma entrada do `Schema`. Teste: um destino com
   coluna que o `Schema` não declara é erro **antes** de qualquer escrita.
3. Campo do registro que nenhuma coluna consome é erro nomeando o campo.
4. `PartitionBy` referencia coluna do `Schema`; um nome que não está lá é erro.
5. Autodetect não aparece em nenhum caminho de criação. `grep AutoDetect` só
   acha o do stage temporário do merge, que é interno e não vira destino.
6. A conferência declarado-vs-real roda **antes do extract**, e roda nos três
   caminhos (inline, GCS, merge) — não só no do dedup, como hoje.
7. `ExtraMetadata` removido; `CHANGELOG` diz o que era e qual declaração o
   substitui, com o `Schema` das seis colunas escrito por extenso, porque é o
   que o consumidor vai copiar.
8. Um teste de integração para cada uma das duas formas do §2 — envelope e
   plana — provando que o SDK escreve as duas sem preferir nenhuma.
9. `go test ./... -short` verde e `go vet ./...` limpo.
10. O `README` do `sdk/` abre pelo `Schema`. Hoje ele abre pelo `Target` sem
    colunas, que é o que ensinou o consumidor a não pensar nelas.

---

## 7. A prova final, fora do repositório

Pegue o `main.go` do consumidor
(`zarv-data-pipeline/scripts/vendors/exemplo_go/main.go`) e responda, lendo só
ele:

> Quantas colunas tem a tabela de destino, quais são os nomes, e o que cai em
> cada uma?

Hoje não dá. Quando der, esta spec está cumprida.
