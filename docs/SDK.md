# SDK — extract e load

O primeiro recurso público do Bravis fora do orquestrador: uma biblioteca Go que
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

### 2.2 pkg.go.dev exige repositório público — e hoje não existe

> **Bloqueio real. Resolver antes de prometer a URL.**
>
> Este repositório não tem remote configurado: o código existe em uma máquina. O
> pkg.go.dev indexa a partir do proxy de módulos, que busca do VCS público — sem
> repositório público, nada é publicado.
>
> Some-se que o Bravis vai sair da Zarv (decisão de 2026-09-01), então
> `github.com/AreteAcademy/bravis` pode não ser o caminho definitivo. **Trocar o
> caminho do módulo depois de publicar é quebra para todo consumidor.**
>
> Ordem correta:
>
> 1. decidir o caminho definitivo do módulo;
> 2. repositório público naquele caminho;
> 3. `git tag sdk/v0.1.0` — a tag de um módulo aninhado leva o prefixo do
>    diretório, e errar isso é o motivo mais comum de "o proxy não acha minha
>    versão";
> 4. `GOPROXY=proxy.golang.org go list -m <caminho>/sdk@v0.1.0` para forçar a
>    indexação.

Enquanto isso não existir, o consumidor usa `replace` para o diretório local.
Funciona e não bloqueia o caso; só não é publicação.

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

## 3. O contrato de saída — não se negocia

O SDK escreve numa tabela de landing com esta forma exata:

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
>
> Escreva esse teste **antes** do resto: uma tabela de casos com valores gerados
> pela implementação Python de referência, conferindo igualdade exata.

`source_key` vazio é **erro**, não aviso. A implementação de referência levanta
exceção; mantenha.

---

## 4. `sdk/extract`

O que "simples e poderoso" tem de significar: **o caso comum em três linhas, e o
caso difícil possível sem sair da biblioteca**.

O caso comum:

```go
linhas, err := extract.CSV(ctx, extract.Fonte{
    URL:       "https://exemplo.gov/api/area/csv/" + chave + "/VIIRS/-74,-34,-34,6/1",
    Cabecalho: http.Header{"User-Agent": {"bravis-sdk"}},
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
| **Load job inline** (NDJSON) | até ~5 mil linhas | uma requisição, sem bucket; é o piso a superar |
| **Load job via GCS** | acima disso | escreve o objeto, dispara o load apontando para `gs://`, apaga. Sem limite prático de tamanho e sem prender a requisição |
| **Storage Write API** | **fora do escopo agora** | é o caminho moderno para *streaming* com exactly-once. O padrão aqui é lote, e misturar dois modelos de consistência numa v1 é dívida. Fase 2. |

O consumidor não escolhe. Ele passa as linhas e o SDK decide — uma API que
obriga a escolher estratégia de carga transfere ao consumidor uma decisão que
depende de conhecer o BigQuery.

### Formatos no staging

**NDJSON, CSV e Parquet.** Parquet é o default para volume: colunar, comprimido,
e o BigQuery lê o schema do próprio arquivo. CSV existe porque alguns
fornecedores já entregam CSV e reescrever é desperdício — mas CSV não carrega
tipo, então `payload` JSON dentro de CSV exige escape e é o pior dos três.
**Documente isso na função, não numa wiki.**

### Requisitos, todos verificáveis

- **`WRITE_APPEND` sempre.** O SDK nunca trunca. Um SDK capaz de
  `WRITE_TRUNCATE` é um SDK capaz de apagar histórico por engano.
- **Bucket de staging por configuração**, com default explícito. Objeto com
  prefixo por data e nome único; TTL no bucket, e apagar depois do load
  bem-sucedido — lixo em bucket é conta que ninguém revisa.
- **Erro que diz o que fazer.** Reporte tabela, contagem de linhas, formato e os
  erros por linha que o BigQuery devolve (`job.Status.Errors`), truncados. "load
  failed" sozinho custa uma investigação.
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
