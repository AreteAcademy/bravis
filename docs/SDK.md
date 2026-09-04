# SDK — extract e load

> **HISTÓRICO — não é o estado atual.** Este documento vale para `sdk/v0.1`.
> É o prompt original de construção, de 2026-09-02. A API mudou por completo desde então — `Fonte` virou `Source`, `ExtraMetadata` virou `Metadata`, `Guard`/`Expand` viraram `Records`, e os drivers viraram valores em `from`/`to`.
>
> Para o SDK como ele é hoje: [`SDK_ARQUITETURA.md`](SDK_ARQUITETURA.md),
> [`SDK_NOVO_DRIVER.md`](SDK_NOVO_DRIVER.md) e [`SDK_DECISOES.md`](SDK_DECISOES.md).

O primeiro recurso público do Brevis fora do orquestrador: uma biblioteca Go que
resolve as duas pontas de um fetcher de dados. **Extract** abstrai a coleta por
HTTP; **load** escreve no BigQuery com as técnicas boas — lote, staging em GCS,
CSV, Parquet.

Vive em `sdk/`, com o seu próprio `go.mod`, e é publicado em pkg.go.dev.

Este documento é o prompt de construção. O lado do consumo — a imagem Go, o
vendor reescrito e a comparação que vai ao time — está em
`zarv-data-pipeline/docs/plan/2026-09-02-vendor-em-go.md`.

---

## 1. Por que existe

Um fetcher de dados é sempre a mesma coisa: pedir por HTTP com retry, decodificar
um formato, e escrever em lote num warehouse. No `zarv-data-pipeline` esse mesmo
código está copiado em 24 vendors Python — cada um com o seu `client.py`,
`models.py`, `fetch.py` e `loader.py`, 180 a 570 linhas por vendor.

O SDK é a tentativa de escrever isso **uma vez**, em Go, e ganhar de lambuja o
que a linguagem dá: um binário estático de 2,4 MB que sobe sem interpretador,
contra os 144 MB da imagem Python. Num pipeline `*/15`, isso são 96
inicializações por dia.

O primeiro consumidor é o `zarv-data-pipeline`. Ele é o teste de que a API é boa:
se escrever um fetcher com o SDK não for visivelmente mais simples que escrever
sem ele, a API está errada.

---

## 2. Decisões tomadas, e o motivo

### 2.1 Módulo aninhado, com `go.mod` próprio

`sdk/go.mod` declarando `module <caminho-definitivo>/sdk`.

Não é preferência de organização. O módulo raiz depende de `pgx`, `goose`,
`cobra` e `templ`; sem um `go.mod` próprio, quem importar o SDK para escrever um
fetcher de 200 linhas baixa o orquestrador inteiro. **Um SDK que arrasta um
driver de Postgres não é adotado.**

### 2.2 Publicado

O módulo está em `github.com/AreteAcademy/brevis/sdk`. Versões no proxy:

| versão | estado |
|---|---|
| `v0.1.0` | **não use.** `go.mod` fixava uma revisão inexistente de `groupcache`; não compila para ninguém. O proxy é imutável, então ela fica lá para sempre |
| `v0.1.1` | primeira que compila |
| `v0.2.0` | paginação, rate limiting, XML e as opções funcionais do `load` |

A armadilha que continua valendo para as próximas versões: **a tag de um módulo
aninhado leva o prefixo do diretório.** É `sdk/v0.2.0`, não `v0.2.0` — errar isso
é o motivo mais comum de "o proxy não acha minha versão".

Fluxo de publicação:

```bash
git tag sdk/v0.2.1
git push origin sdk/v0.2.1
# o workflow publish-sdk.yml faz o resto
```

Duas lições que custaram uma versão queimada, ambas agora automatizadas em
`.github/workflows/publish-sdk.yml`:

1. **O proxy é imutável.** Depois que uma versão é buscada uma vez, o conteúdo
   dela está congelado — apagar a tag no git não desfaz nada. Por isso existe um
   gate que compila um consumidor descartável **antes** do release: é o último
   ponto onde um `go.mod` ruim ainda é recuperável.
2. **URL do proxy usa case-encoding.** `AreteAcademy` vira `!arete!academy`. O
   passo de verificação original montava a URL na mão, dava 404 sempre e saía com
   `exit 0` — verificação que não pode falhar. Use `go list -m`, que codifica
   sozinho.

### 2.3 `iter.Seq2` entre extract e load, não slices

O Go aqui é 1.25. Use `iter.Seq2[Envelope, error]`.

Um vendor pode devolver muito mais do que cabe em memória — no consumidor atual,
uma única tabela de telemetria tem 290 GiB e 1,7 bilhão de linhas. Uma API que
devolve `[]Envelope` ensina o consumidor a materializar tudo, e range-over-func
resolve isso sem obrigar ninguém a escrever callback.

### 2.4 Stdlib primeiro

`net/http`, `encoding/json`, `encoding/csv`, `log/slog`. As exceções aceitáveis,
por não haver equivalente na stdlib:

| dependência | para quê |
|---|---|
| `cloud.google.com/go/bigquery` | cliente oficial |
| `cloud.google.com/go/storage` | staging em GCS |
| `github.com/google/uuid` | UUID v5 |
| Parquet | ver abaixo |

Para Parquet, escolha entre a biblioteca do Arrow (canônica, pesada) e a
`parquet-go` (pequena, suficiente para escrever). **Meça o custo em bytes da
imagem final antes de decidir.** Se Parquet dobrar a imagem, ele passa a ser um
subpacote opcional (`sdk/load/parquet`) e sai do núcleo — o argumento inteiro do
SDK é o tamanho do binário.

Nada de framework de retry: o equivalente em Python são 60 linhas de backoff. Em
Go é menos.

---

## 3. O contrato de saída

O `load` é **agnóstico de schema por padrão**: escreve o payload como veio e
recusa criar a tabela. Quem define as colunas é quem usa o SDK.

Mas o `ingestion_id` continua sendo contrato, e contrato precisa de um dono
único. Por isso existe o **modo envelope**, opt-in:

```go
loader, _ := load.New(ctx, nil,
    sdk.WithProjectID("proj"),
    sdk.WithDataset("landing"),
    sdk.WithTable("vendors_acme_transactions"),
    sdk.WithEnvelopeColumns(true),   // emite as 6 colunas abaixo
)
```

Com ele ligado, o SDK escreve nesta forma exata:

```sql
CREATE TABLE IF NOT EXISTS <dataset>.vendors_<vendor>_<entidade>s (
  ingestion_id        STRING NOT NULL,
  ingestion_loaded_at TIMESTAMP NOT NULL,
  provider            STRING NOT NULL,
  entity              STRING NOT NULL,
  source_key          STRING,
  payload             JSON   NOT NULL
)
PARTITION BY DATE(ingestion_loaded_at)
CLUSTER BY provider, entity;
```

**Por que opt-in e não padrão.** O desenho agnóstico é bom: a maioria dos casos
não quer um envelope imposto. Mas espalhar a montagem das 6 colunas por cada
consumidor faria o `ingestion_id` perder o dono único — e era exatamente essa
unicidade que evitava a duplicação. Com o modo envelope, quem quer o contrato o
pede, e um único lugar calcula o id.

As três formas de escrever, e quando usar cada uma:

| modo | escreve | quando |
|---|---|---|
| padrão | o payload, cru | você define o schema e não quer nada imposto |
| `WithMetadata(true)` | payload + campos `_brevis_*` misturados nele | quer a proveniência junto do dado, num objeto plano |
| `WithEnvelopeColumns(true)` | as 6 colunas, com o payload aninhado em `payload` | precisa casar com a camada bronze do `zarv-data-pipeline` |

Os dois últimos são mutuamente exclusivos e `New` recusa os dois juntos — são
duas respostas diferentes para a mesma pergunta.

### O `ingestion_id` é contrato, não detalhe de implementação

```
namespace = e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7
chave     = provider|entity|source_key|record_ts
id        = UUID v5 (namespace, chave)
```

> **Reproduza byte a byte.** É a chave de idempotência: a camada bronze do
> consumidor deduplica por `ingestion_id`. Um UUID v5 com namespace diferente,
> separador diferente ou `record_ts` formatado diferente faz a linha escrita pelo
> SDK **não** casar com a escrita pelo fetcher Python equivalente — e a mesma
> leitura entra duas vezes.

Conferido contra `uuid.uuid5` do Python em 2026-09-02; os UUIDs batem
exatamente. O modo envelope usa `Envelope.IngestionID()` — a mesma função, não
uma cópia — e há teste que falha se as duas divergirem.

`source_key` vazio é **erro**, não aviso, nos dois modos que calculam o id.

---

## 4. `sdk/extract`

O que "simples e poderoso" tem de significar: **o caso comum em três linhas, e o
caso difícil possível sem sair da biblioteca**.

O caso comum:

```go
linhas, err := extract.CSV(ctx, extract.Fonte{
    URL:       "https://exemplo.gov/api/area/csv/" + chave + "/VIIRS/-74,-34,-34,6/1",
    Cabecalho: http.Header{"User-Agent": {"brevis-sdk"}},
})
```

**Campos zero têm de ser úteis.** `extract.Fonte{URL: x}` com o resto vazio
precisa funcionar com defaults sensatos (GET, 3 tentativas, 30 s). Configuração
obrigatória para o caso comum é o que faz um SDK não ser usado.

O que a biblioteca resolve, e que hoje está copiado em cada cliente:

| responsabilidade | requisito |
|---|---|
| **Retry com backoff** | exponencial com jitter; só em 429, 5xx e erro de rede. `4xx` que não 429 **não** se retenta — é pedido errado, e insistir multiplica o erro. Respeite `Retry-After`. |
| **Timeout** | por tentativa **e** total, separados. Um total sem por-tentativa deixa uma conexão pendurada consumir o orçamento inteiro. |
| **Um 200 que não é dado** | `Guarda func(*http.Response, []byte) error`, chamada antes de decodificar. API de governo devolve `200` com uma linha de texto puro no lugar do cabeçalho CSV; sem isso o erro entra no warehouse como dado. |
| **Formatos** | JSON, NDJSON, CSV (com e sem cabeçalho), XML. Decodificação em **stream**, nunca `io.ReadAll`. |
| **Paginação** | uma interface, não um `for` no consumidor: cursor, offset e `Link` (RFC 5988). Devolve `iter.Seq2`. |
| **Limite de taxa** | `*rate.Limiter` opcional na Fonte. Vários fornecedores publicam limite e punem quem passa. |
| **Observabilidade** | `slog` com URL, status, tentativa, duração e bytes. **A URL vai redigida** — chave de API em query string é o caso comum, e vazá-la em log é incidente. |

---

## 5. `sdk/load`

```go
type Envelope struct {
    Provider  string
    Entity    string
    SourceKey string
    RecordTS  string  // entra no ingestion_id; formate UMA vez, aqui dentro
    Payload   any     // vira a coluna JSON
}
```

`Envelope.IngestionID()` implementa a §3 e é o **único** lugar que calcula aquele
UUID.

### Três estratégias, e a escolha é do SDK

| estratégia | quando | por que |
|---|---|---|
| **Load job inline** (NDJSON) | até ~5 mil linhas | uma requisição, sem bucket; é o piso a superar. Load job de verdade, não streaming insert — ver nota abaixo |
| **Load job via GCS** | acima disso | escreve o objeto, dispara o load apontando para `gs://`, apaga. Sem limite prático de tamanho e sem prender a requisição |
| **Storage Write API** | **fora do escopo agora** | é o caminho moderno para *streaming* com exactly-once. O padrão aqui é lote, e misturar dois modelos de consistência numa v1 é dívida. Fase 2. |

O consumidor não escolhe. Ele passa as linhas e o SDK decide — uma API que
obriga a escolher estratégia de carga transfere ao consumidor uma decisão que
depende de conhecer o BigQuery.

> **As duas são load job.** A inline passou por `table.Inserter()` até a v0.2.1,
> que é a *streaming insert API*: cobrada por linha, e as linhas ficam num buffer
> onde o DML não as enxerga por até 90 minutos. A estratégia dizia `"inline"` e a
> documentação dizia "load job", mas o modelo de consistência não era nenhum dos
> dois. Hoje é `LoaderFrom` sobre um `ReaderSource` com o NDJSON em memória —
> mesmo modelo da via GCS, mudando só de onde o BigQuery lê.

### Formatos no staging

**Hoje: só NDJSON.** `LoadConfig.Format` aceita `"ndjson"` e recusa `"csv"` e
`"parquet"` com um erro que diz "not implemented in this version".

Isso é deliberado. Antes o campo aceitava os três, todo caminho escrevia NDJSON
de qualquer jeito, e o `LoadResult.Format` devolvia o valor configurado — então
`WithFormat("parquet")` reportava uma carga Parquet que nunca aconteceu. **Número
errado na telemetria é pior que número ausente**, porque ninguém desconfia dele.
Uma API que recusa o que não faz é confiável; uma que aceita e ignora, não.

O que os dois valeriam quando forem implementados:

- **Parquet** seria o default para volume: colunar, comprimido, e o BigQuery lê
  o schema do próprio arquivo. Mas exige dependência — **meça o custo em bytes
  da imagem antes**, porque o argumento do SDK é o tamanho do binário. Se
  dobrar, Parquet vira subpacote opcional (`sdk/load/parquet`).
- **CSV** existe porque alguns fornecedores já entregam CSV e reescrever é
  desperdício — mas CSV não carrega tipo, então `payload` JSON dentro de CSV
  exige escape e é o pior dos três.

`LoadResult.Format` reporta o formato **efetivamente escrito**.

### Requisitos, todos verificáveis

- **`WRITE_APPEND` sempre.** O SDK nunca trunca. Um SDK capaz de
  `WRITE_TRUNCATE` é um SDK capaz de apagar histórico por engano.
- **Bucket de staging por configuração**, com default explícito. Objeto com
  prefixo por data e nome único; TTL no bucket, e apagar depois do load
  bem-sucedido — lixo em bucket é conta que ninguém revisa.
- **Erro que diz o que fazer.** Reporte tabela, contagem de linhas, formato e os
  erros por linha que o BigQuery devolve (`job.Status.Errors`), truncados. "load
  failed" sozinho custa uma investigação. `Load` devolve um `LoadResult` **junto
  com** o erro — nunca `nil` —, porque a forma documentada de ler o diagnóstico é
  `result.ErrorRows` depois de um erro não-nulo.
- **`Resultado`** com linhas escritas, bytes, duração e estratégia usada. É o que
  permite **medir** a diferença contra a implementação Python em vez de afirmá-la.
- **Idempotência é do bronze, não do load.** O mesmo lote carregado duas vezes
  produz linhas duplicadas na landing, e é o `ingestion_id` que resolve a
  jusante. Diga isso na documentação do pacote, para ninguém procurar dedup no
  lugar errado.

---

## 6. Testes

**Extract** — `net/http/httptest`, sem rede. Os oito casos que não podem faltar:

1. retry em 429 e em 5xx;
2. **não**-retry em 400;
3. `Retry-After` respeitado;
4. timeout por tentativa distinto do total;
5. a guarda de 200-que-não-é-dado;
6. CSV sem cabeçalho;
7. paginação de duas páginas;
8. cancelamento por contexto no meio do stream.

**Load** — o cliente do BigQuery atrás de uma interface pequena, com um duplo em
memória. O teste que não pode faltar é o do `ingestion_id` (§3).

**Integração** — um teste marcado com `testing.Short()`, que roda contra um
BigQuery real só quando pedido. É o que prova que o schema bate; o duplo em
memória não prova isso.

---

## 7. Critério de pronto

1. `sdk/` compila e testa isolado, com `go.mod` próprio.
2. O teste do `ingestion_id` passa contra valores da implementação de referência.
3. `extract` cobre os oito casos da §6 sem tocar a rede.
4. `load` escreve nas três estratégias e o schema bate.
5. `go vet` limpo e a documentação de pacote (`doc.go`) explica **quando não
   usar** o SDK, não só quando usar.
6. O caminho do módulo está decidido e o repositório público existe — ou o item
   está explicitamente adiado, com o `replace` documentado como temporário.

## 8. Fora de escopo, dito para não parecer esquecimento

- **Storage Write API** — fase 2 (§5).
- **Outros destinos** (Postgres, S3). O carregador é interface para permitir isso
  depois, não para entregar agora.
- **Transformação.** O SDK extrai e carrega. Transformar é trabalho do dbt no
  consumidor, e um SDK que também transforma deixa de ter fronteira.
