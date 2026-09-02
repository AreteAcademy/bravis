# SDK — conserto do `load`

**Aberto em** 2026-09-02 · **Versão analisada** `v0.1.1` · **Alvo** `v0.1.2`

Spec de conserto, escrita para ser executada. Cada item traz o arquivo e a linha,
o que o código faz hoje, o que precisa fazer, e como provar que ficou certo.

O `extract` está utilizável e não é o assunto aqui — ver §6 para o que falta
nele. O `load`, não: **o caminho padrão não escreve uma linha.**

---

## Estado verificado do `v0.1.1`

O que a `v0.1.1` consertou em relação à `v0.1.0`, conferido item por item:

| defeito da v0.1.0 | estado |
|---|---|
| Não compilava — 4 imports não usados | **resolvido**, binário gera |
| `gcsRef.Format` / `bigquery.NDJSON` inexistentes | removidos |
| Teste importava `github.com/zarvhq/bravis/sdk` | nenhuma ocorrência |
| 5 versões indiretas em revisões inexistentes | **`go mod tidy` de consumidor passa** |

E o contrato de idempotência continua correto — `IngestionID()` foi conferido
contra `uuid.uuid5` do Python com os mesmos casos e os UUIDs batem exatamente.

---

## 1. `loadInline` falha em runtime para qualquer lote — **bloqueante**

**`load/load.go:202`**

```go
rows[i] = &bigquery.StructSaver{
    Struct: json.RawMessage(data),
}
```

`StructSaver.Struct` exige struct ou ponteiro para struct. `json.RawMessage` é
`[]byte`. Provado executando `Save()` diretamente:

```
bigquery: type is json.RawMessage, need struct or struct pointer
```

Isto **não** é um caso de borda: `loadInline` é a estratégia escolhida para todo
lote de até `ThresholdForGCS` (5000) linhas, que é praticamente todo fetcher. A
biblioteca não escreveu uma linha ainda.

**O conserto** é trocar o saver por um que aceite dados dinâmicos. Duas saídas:

- `bigquery.ValueSaver` implementado sobre `map[string]bigquery.Value` — é o
  caminho natural quando a forma da linha só se conhece em runtime;
- ou abandonar o `Inserter` e usar um load job, o que resolve o item 4 de uma vez.

**Como provar:** um teste que chama `Save()` no que `loadInline` monta e confere
que devolve `err == nil` e um mapa com as chaves esperadas. Hoje esse teste falha
na primeira linha, e é por isso que ele tem de existir.

---

## 2. `loadViaGCS` não define o formato do arquivo — **bloqueante**

**`load/load.go:252`**

```go
gcsRef := bigquery.NewGCSReference(fmt.Sprintf("gs://%s/%s", l.cfg.StagingBucket, objName))
// falta: gcsRef.SourceFormat
loader := table.LoaderFrom(gcsRef)
```

`NewGCSReference` deixa `SourceFormat` vazio, e o BigQuery trata vazio como
**CSV**. O arquivo escrito acima (linha 218) é NDJSON, com sufixo `.ndjson`. O
load job tenta ler NDJSON como CSV.

**O conserto:** definir explicitamente, derivando de `cfg.Format` em vez de
fixar:

```go
switch l.cfg.Format {
case "ndjson", "":
    gcsRef.SourceFormat = bigquery.JSON
case "csv":
    gcsRef.SourceFormat = bigquery.CSV
case "parquet":
    gcsRef.SourceFormat = bigquery.Parquet
}
```

Isso amarra o item 3.

**Como provar:** teste de integração (`testing.Short()`) que carrega 3 linhas via
GCS numa tabela real e faz `SELECT COUNT(*)`. Sem `SourceFormat` correto o job
falha, então o teste vira a prova.

---

## 3. `Format` aceita `csv` e `parquet`, e escreve NDJSON — a telemetria mente

**`load/load.go:50-51`, `218`, `132`**

`New` aceita `Format` como `"ndjson"`, `"csv"` ou `"parquet"`. Mas
`loadViaGCS:218` monta o nome do objeto sempre como `.ndjson` e escreve NDJSON
linha por linha. E `Load:132` devolve `Format: l.cfg.Format`.

Então `WithFormat("parquet")` produz `LoadResult.Format == "parquet"` enquanto
grava NDJSON. **Quem medir vai acreditar que testou Parquet.** Um número errado
na telemetria é pior que um número ausente, porque ninguém desconfia dele.

**O conserto, e a escolha é de escopo:**

- **Mínimo honesto:** `New` **recusa** `csv` e `parquet` com um erro que diz "não
  implementado nesta versão". Uma API que rejeita o que não faz é confiável; uma
  que aceita e ignora, não.
- **Completo:** implementar os dois. Parquet exige uma dependência — meça o custo
  em bytes da imagem final antes, porque o argumento do SDK é o tamanho do
  binário. Se dobrar, Parquet vira subpacote opcional (`sdk/load/parquet`).

Faça o mínimo agora e abra issue para o completo. `LoadResult.Format` passa a
reportar o formato **efetivamente escrito**, não o configurado.

---

## 4. O caminho inline é streaming insert, não lote

**`load/load.go:207`** — `table.Inserter()`.

`Inserter` é a *streaming insert API*: custo por linha, buffer de streaming, e as
linhas não ficam disponíveis para DML imediatamente. A documentação do pacote
descreve o inline como "load job", e o `LoadResult.Strategy` diz `"inline"`, o que
sugere lote.

São modelos de consistência diferentes, e o padrão deste SDK é lote (a decisão de
deixar a Storage Write API fora da v1, em `SDK.md` §5, é justamente por isso).

**O conserto:** trocar por um load job com os dados embutidos — `LoaderFrom` sobre
um `bigquery.ReaderSource` com NDJSON em memória. Resolve o item 1 no mesmo
movimento: sem `Inserter`, não há `StructSaver`.

Se houver motivo para manter streaming, o motivo tem de estar escrito no código e
na documentação, e o `Strategy` tem de dizer `"streaming"`.

---

## 5. O contrato das 6 colunas passou para o consumidor — decidir de quem é

A `v0.1.1` mudou de desenho, e a documentação do pacote agora afirma:

> The SDK does NOT impose a schema — you define it.

É uma decisão legítima e o loader é coerente com ela: `Load` **verifica** que a
tabela existe e recusa criá-la ("Create it manually with your desired schema").

Mas duas coisas ficaram inconsistentes com isso:

**5.1 `SDK.md` §3 está desatualizado.** Ele declara o schema de 6 colunas como
"não se negocia" e diz que o SDK escreve nessa forma. Com o desenho agnóstico,
isso passou a ser responsabilidade do consumidor. **Corrija a §3** para dizer de
quem é o contrato — texto que descreve um comportamento que não existe é pior que
texto ausente.

**5.2 `AddMetadata` não produz as 6 colunas.** Em `load.go:170` os metadados são
mesclados **para dentro** do payload com chaves prefixadas:

```go
"_bravis_ingestion_id", "_bravis_ingestion_loaded_at",
"_bravis_provider", "_bravis_entity", "_bravis_source_key", "_bravis_record_ts"
```

O resultado é um objeto plano com `_bravis_*` misturado ao dado da fonte — não as
colunas de primeiro nível `ingestion_id`, `ingestion_loaded_at`, `provider`,
`entity`, `source_key`, `payload` que o consumidor `zarv-data-pipeline` lê no
`metadata_vendor()` do dbt.

**A decisão a tomar** (e ela é de produto, não de implementação):

| opção | consequência |
|---|---|
| **Modo envelope opt-in no SDK** — `WriteEnvelopeColumns: true` emite as 6 colunas | o SDK continua agnóstico por padrão, e quem quer o contrato o pede. Um único lugar calcula e escreve o `ingestion_id`, que é o que garante que linha em Go casa com linha em Python |
| **Deixar no consumidor** | cada consumidor remonta as 6 colunas. Funciona, mas o `ingestion_id` deixa de ter um dono único — e era essa unicidade que evitava duplicação |

**Recomendação: modo envelope opt-in.** O argumento inteiro de tratar o
`ingestion_id` como contrato é que exista **um** lugar que o produz. Espalhar isso
por consumidor reabre exatamente o risco que a §3 do `SDK.md` levantou.

---

## 6. Fora do `load`: declarados e não implementados no `extract`

Campos que existem em `types.go` e são lidos em **nenhum** lugar do código:

| campo | verificado em v0.1.1 |
|---|---|
| `CursorKey` | 1 ocorrência — só a declaração |
| `OffsetKey` | 1 ocorrência — só a declaração |
| `PageSize` | 1 ocorrência — só a declaração |
| `RateLimiter` | 1 ocorrência — só a declaração |
| `HasHeader` | **0 ocorrências** |
| `Retry-After` | 1 ocorrência, num comentário `// Implementar retry com Retry-After` |

A documentação do pacote lista os seis como recursos. **Campo público que não faz
nada é pior que campo ausente**, porque quem o preenche acredita ter configurado
algo.

Escolha uma por campo: implementar, ou remover da struct e da documentação até
existir. Não deixe declarado.

Implementado e funcionando, conferido: `Guard`, `Timeout` por tentativa,
`TotalTimeout`, retry com backoff exponencial e `JitterFraction`.

---

## 7. `SDK.md` §2.2 também ficou desatualizado

Ela diz "pkg.go.dev exige repositório público — e hoje não existe" e trata a
publicação como bloqueio. O módulo **está publicado** em
`github.com/AreteAcademy/bravis/sdk`, com `v0.1.0` e `v0.1.1` no proxy. Reescreva
como registro do que foi feito, mantendo a armadilha da tag com prefixo de
diretório — ela continua valendo para as próximas versões.

---

## 8. Critério de pronto para a `v0.1.2`

1. `loadInline` escreve linhas de verdade, com teste que exercita o saver.
2. `loadViaGCS` define `SourceFormat` a partir de `cfg.Format`.
3. `Format` não aceita valor que não implementa, e `LoadResult.Format` reporta o
   formato **escrito**.
4. A estratégia inline é load job — ou o streaming está justificado no código e o
   `Strategy` diz `"streaming"`.
5. Decidido e implementado quem produz as 6 colunas (§5).
6. Nenhum campo público sem implementação: os seis da §6 estão implementados ou
   removidos.
7. Um teste de integração com `testing.Short()` carrega e conta linhas numa
   tabela real, nas duas estratégias. É o único que prova que o schema bate — o
   duplo em memória não prova.
8. `SDK.md` §2.2 e §3 atualizados.
