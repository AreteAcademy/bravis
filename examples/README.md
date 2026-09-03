# Bravis SDK — exemplos

Cada exemplo é um módulo executável próprio. O `go.mod` daqui aponta para
`../sdk` via `replace`, então eles compilam contra a árvore de trabalho — o CI
os constrói e testa a cada push, o que faz deles um portão sobre a API, não
apenas documentação.

```bash
cd examples
go build ./...   # todos compilam
go test ./...    # o 05 tem testes de verdade
```

## Extract

### [01-basic-extract](01-basic-extract/) — o caso mínimo

```bash
go run ./01-basic-extract -url https://example.gov/data.csv
```

`extract.CSV` com configuração zero. A primeira linha do CSV vira as chaves;
`NoHeader: true` trata todas as linhas como dado, com chaves `field_0`,
`field_1`…

### [02-advanced-extract](02-advanced-extract/) — o que importa contra uma API real

Headers, os dois timeouts (por tentativa e total), retry com backoff, um
`Guard` que rejeita 200 com corpo de erro, e rate limiting.

O `RateLimiter` aceita qualquer coisa com `Wait(ctx) error` — inclusive
`*rate.Limiter` de `golang.org/x/time/rate`, sem o SDK carregar a dependência.

## Load

### [03-basic-load](03-basic-load/) — escrever no BigQuery

```bash
go run ./03-basic-load -project meu-projeto -dataset landing -table raw_data
```

A tabela precisa existir: o SDK não é dono do seu schema. Mostra as opções
funcionais e `WithMetadata`, que dobra os campos `_bravis_*` para dentro do
payload.

### [07-envelope-columns](07-envelope-columns/) — o contrato de 6 colunas

Quando as linhas precisam casar com uma camada bronze que deduplica por
`ingestion_id`. `WithEnvelopeColumns(true)` embrulha o payload nas colunas
`ingestion_id`, `ingestion_loaded_at`, `provider`, `entity`, `source_key`,
`payload`.

Existe para o `ingestion_id` ter **um** dono: remontar essas colunas em cada
consumidor faz os ids divergirem, que é a duplicação que o contrato evita.

## Transform

### [09-transform](09-transform/) — o passo entre extract e load

```bash
go run ./09-transform -dry-run
```

`Without` para descartar metadado de requisição, `Rename` para dar aos campos
o nome que você usa, `Compute` para derivar, e uma função sua para o resto —
com `sdk.SkipRecord` para filtrar.

`Target.Key` e `Target.When` leem o payload **depois** de todo Transformer, então
apontam para o nome novo. Apontar para o antigo é erro listando o que o registro
tem de fato — e não uma chave curta, que mudaria todo `ingestion_id` em silêncio.

## Pipeline

### [04-complete-pipeline](04-complete-pipeline/) — extract → load paginado

Percorre uma API paginada por `Link: rel="next"` e carrega em lotes de mil, para
a memória ficar plana independente do tamanho total.

As três estratégias de paginação:

```go
sdk.Source{URL: url, FollowLinks: true}                              // Link header
sdk.Source{URL: url, CursorKey: "next_page", DataKey: "results"}     // cursor no corpo
sdk.Source{URL: url, OffsetKey: "offset", PageSize: 100}             // offset
```

Todas param em `MaxPages` (mil por padrão), para um servidor que sempre anuncia
próxima página não girar para sempre.

## Operação

### [05-testing](05-testing/) — como testar código que usa o SDK

O único com testes rodáveis:

```bash
go test ./05-testing -v
```

`httptest.Server` no lugar da API real: rápido, offline, determinístico. Cobre
retry num 503, ausência de retry num 404, e a propagação de erro do seu próprio
processamento.

### [06-config-from-env](06-config-from-env/) — configuração por ambiente

```bash
export BRAVIS_PROJECT=meu-projeto
export BRAVIS_DATASET=landing
go run ./06-config-from-env
```

Como isso normalmente roda no Kubernetes.

## Autenticação

Os exemplos de load precisam de credenciais GCP:

```bash
gcloud auth application-default login
# ou
export GOOGLE_APPLICATION_CREDENTIALS=/caminho/para/credenciais.json
```
