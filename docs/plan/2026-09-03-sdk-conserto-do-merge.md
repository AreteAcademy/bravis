# Conserto do `MERGE` do `load` — itens 1 e 2 do `SDK_V9.md`

**Escrito em** 2026-09-03 · **Base** `sdk/v0.10.1` · **Alvo** `sdk/v0.10.2`

> **CONCLUÍDA em 2026-09-03, entregue na `sdk/v0.12.0`** (a `v0.12.1` corrige um
> exemplo do README que não compilava). Os dez critérios do §4 estão atendidos,
> com duas ressalvas que são acertos e não faltas:
>
> - **Critérios 2 e 3 caíram por construção.** O item 1 foi resolvido fazendo o
>   `DedupMerge` ceder ao caminho comum quando a tabela não existe, em vez de
>   criar o destino a partir do schema da temporária. Como não há um segundo
>   lugar criando tabela, não há `layout()` a extrair nem `409` a tratar. A
>   solução é mais simples que a proposta aqui.
> - **`TestIntegrationGCSStrategy` fica em SKIP** sem `BRAVIS_IT_BUCKET`. É a
>   estratégia dos lotes acima de 5000 linhas, e é onde vive o conserto do
>   `KeepStagedFile` — definir essa variável no ambiente automatizado é o que
>   falta para o §0 valer para o pacote inteiro.
>
> Rodar os testes de integração pela primeira vez achou **quatro** defeitos além
> dos dois desta spec: `CreateTable` com `DedupMerge`, `Load` mutando a fatia do
> chamador (que quebrava exatamente o retry que o `DedupMerge` existe para
> tratar), `ClusterBy` falhando só depois do job submetido, e `DeleteAfterLoad`
> nunca limpando — documentado como `default: true`, que um `bool` não consegue
> ser. Ver `3f4d762`.

Spec escrita para ser executada. Os itens 3 (telemetria que mente) e 4 (as seis
colunas) do [`SDK_V9.md`](../SDK_V9.md) **ficam fora**: são de outra natureza, e
misturá-los aqui atrasa os dois que travam um consumidor real hoje.

O `sdk/load/` está intocado desde a `v0.9.0` — `git diff sdk/v0.9.0 HEAD --
sdk/load/` não devolve nada. Nada do que está abaixo foi tentado ainda.

---

## 0. Antes de escrever qualquer linha: reproduza

**O teste que pega o item 1 já existe.** Isto é o mais importante deste
documento, e é uma correção do que eu mesmo escrevi no `SDK_V9.md`:

`integration_test.go:191`, `TestIntegrationMergeDoesNotDouble`, monta
exatamente a combinação que falha —

```go
name := fmt.Sprintf("it_merge_%d", time.Now().UnixNano())
table := client.Dataset(env.dataset).Table(name)
t.Cleanup(func() { _ = table.Delete(context.Background()) })   // nunca cria

loader, err := New(ctx, nil,
    core.WithExtraMetadata(true),
    core.WithCreateTable(true),
    core.WithDedup(core.DedupMerge),
)
```

Tabela ausente, `CreateTable` ligado, `DedupMerge` ligado. Ele **deve** falhar
com o 404 de hoje. Nunca rodou porque `requireIntegration`
(`integration_test.go:35`) pula sem `BRAVIS_IT_PROJECT`:

```bash
export BRAVIS_IT_PROJECT=<projeto-gcp>
export BRAVIS_IT_DATASET=bravis_it        # opcional, este e o default
go test ./sdk/load/ -run TestIntegrationMergeDoesNotDouble -v
```

Espere:

```
waiting for merge: googleapi: Error 404: Not found: Table
<projeto>:bravis_it.it_merge_<nano> was not found in location US
```

**Se ele passar, pare e reabra o diagnóstico** — significa que o ambiente
difere do que medi, e consertar às cegas a partir daqui é palpite.

A lição a levar: o defeito não escapou por falta de teste. Escapou porque o
teste que o cobre está atrás de uma variável de ambiente que ninguém define.
Qualquer conserto que não deixe esse teste rodando em algum lugar automatizado
deixa a próxima regressão passar do mesmo jeito.

---

## 1. Item 1 — o destino não é criado no caminho do `DedupMerge`

### O que o código faz

`load.go:202` chama `prepareTable`, que em `table.go:44` devolve `(false, nil)`
quando a tabela falta e `CreateSQL` está vazio, delegando a criação ao load job:

```go
if l.cfg.CreateSQL == "" {
    // The load job creates it, inferring the schema from the data.
    return false, nil
}
```

Quem cumpre essa promessa é `applyLayout` (`table.go:90`), chamado em dois
lugares:

```
load.go:419   loadInline
load.go:457   loadViaGCS
```

O ramo do dedup (`load.go:214`) não passa por nenhum. Em `dedup.go:50` o load
job escreve na **temporária**; o destino nunca recebe `CreateDisposition`, e o
`MERGE` de `dedup.go:65` é a primeira instrução a tocá-lo — 404.

Depois, `load.go:241` reporta `TableCreated` a partir do `existed` que
`prepareTable` devolveu, não do que aconteceu.

### O que precisa fazer

Quando o destino estiver ausente, `CreateTable` ligado e `CreateSQL` vazio:
criar o destino **a partir do schema da temporária**, depois do stage e antes do
`MERGE`. Nesse ponto a temporária já tem o schema certo, vindo do autodetect —
é a mesma inferência que o caminho sem dedup usaria, só que já materializada.

### Como implementar, e a armadilha

`applyLayout` decide particionamento e clusterização configurando um
`*bigquery.Loader`. O caminho novo precisa das **mesmas** decisões num
`*bigquery.TableMetadata`. Não duplique: extraia o miolo.

```go
// table.go
func (l *Loader) layout() (*bigquery.TimePartitioning, *bigquery.Clustering) {
    var tp *bigquery.TimePartitioning
    if l.cfg.ExtraMetadata {
        tp = &bigquery.TimePartitioning{
            Type:                   bigquery.DayPartitioningType,
            Field:                  metadataLoadedAt,
            Expiration:             l.cfg.PartitionExpiration,
            RequirePartitionFilter: l.cfg.RequirePartitionFilter,
        }
    }
    var cl *bigquery.Clustering
    if len(l.cfg.ClusterBy) > 0 {
        cl = &bigquery.Clustering{Fields: l.cfg.ClusterBy}
    }
    return tp, cl
}
```

`applyLayout` passa a consumir `layout()`, e o caminho do dedup também. Duas
cópias dessa decisão divergiriam, e o modo de falhar é caro e silencioso: uma
landing sem partição custa varredura cheia em todo `MERGE` que o bronze roda —
**58,96 GiB contra 0,0 GiB de `SELECT`** num consumidor, medição que está no
comentário de `table.go:86`. Um destino criado pelo caminho do dedup sem
partição, enquanto o caminho sem dedup particiona, é exatamente esse custo
aparecendo em metade dos pipelines.

Em `loadWithMerge`, depois do `runLoadJob` do stage (`dedup.go:53`) e antes de
montar o SQL:

```go
if !existed && l.cfg.CreateTable && l.cfg.CreateSQL == "" {
    md, err := temp.Metadata(ctx)
    if err != nil {
        return 0, 0, nil, fmt.Errorf("reading the temporary table's schema: %w", err)
    }
    tp, cl := l.layout()
    if err := table.Create(ctx, &bigquery.TableMetadata{
        Schema:           md.Schema,
        TimePartitioning: tp,
        Clustering:       cl,
    }); err != nil {
        return 0, 0, nil, fmt.Errorf("creating %s from the staged schema: %w", nameOf(table), err)
    }
}
```

`existed` vem de `prepareTable` e hoje morre em `load.go:202`. **Passe-o** para
`loadWithMerge` em `load.go:215` em vez de reler a metadata — a releitura é uma
chamada de rede a mais e abre uma janela de corrida entre a checagem e o
`Create`.

Trate `409 Already Exists` no `Create` como sucesso: duas runs do mesmo step
podem correr juntas, e perder uma carga porque a outra criou a tabela primeiro é
falha inventada. Há `isNotFound` em `table.go`; um `isAlreadyExists` no mesmo
lugar fica coerente.

`load.go:241` precisa refletir o que aconteceu de fato, incluindo neste caminho.

### Como provar

1. `TestIntegrationMergeDoesNotDouble` passa — o teste do §0, sem alterá-lo.
   Se você precisar mudá-lo para passar, o conserto está errado.
2. Um teste unitário de que `applyLayout` e o caminho do dedup consomem o mesmo
   `layout()`. Os testes de layout existentes (`load_test.go:665` a `:713`) são
   o molde.
3. Um teste de integração novo confirmando que o destino criado pelo caminho do
   dedup **está particionado** — o custo do §1 não aparece em contagem de
   linhas, só na metadata.

---

## 2. Item 2 — `INSERT ROW` casa por posição, e o comentário afirma o contrário

### O que o código faz

`dedup.go:60`:

```go
// INSERT ROW rather than a column list: the SDK does not know your
// payload, and BigQuery matches the columns by name.
```

**O BigQuery casa por posição.** Provado direto:

```sql
CREATE TABLE staging._t_alvo  (a STRING, b INT64);
CREATE TABLE staging._t_ordem (b INT64, a STRING);   -- mesmos nomes, ordem trocada
INSERT staging._t_ordem VALUES (7, 'sete');

MERGE staging._t_alvo t USING staging._t_ordem i
  ON t.a = i.a
  WHEN NOT MATCHED THEN INSERT ROW;
```
```
Value has type INT64 which cannot be inserted into column a,
which has type STRING
```

Se casasse por nome, isso passaria. Como o schema da temporária vem do
autodetect sobre o payload (`dedup.go:48`), a ordem das colunas não está sob
controle de quem chama — e o acerto de hoje é coincidência. Num consumidor com
landing de schema fixo:

```
Value has type FLOAT64 which cannot be inserted into column
ingestion_id, which has type STRING
```

`latitude` caiu em `ingestion_id`.

### O que precisa fazer

Nomear as colunas. Elas são conhecidas em runtime: o schema do **destino** é a
autoridade, porque é ele que tem de ser satisfeito, e os valores vêm de
`incoming`.

```sql
WHEN NOT MATCHED THEN
  INSERT (`c1`, `c2`, `c3`) VALUES (incoming.`c1`, incoming.`c2`, incoming.`c3`)
```

**Crase em todo identificador, sem exceção.** Não é zelo: `full`, `range` e
`comment` são reservadas no BigQuery e aparecem em colunas de consumidor de
verdade. Uma coluna chamada `range` sem crase transforma o conserto num erro de
sintaxe que só aparece no cliente que tem essa coluna.

### A regra de reconciliação, e por que ela é assimétrica

Compare os dois schemas **antes** de montar o SQL e decida assim:

| situação | o que fazer | por quê |
|---|---|---|
| coluna em `incoming` que o destino **não** tem | **erro**, nomeando a coluna | é dado sendo descartado em silêncio — o pior modo de falhar, porque some sem sinal |
| coluna no destino que `incoming` **não** tem | seguir; ela fica NULL | legítimo: uma landing pode ter coluna que este payload não preenche, como `source_key` |
| tipos incompatíveis no mesmo nome | **erro**, nomeando a coluna e os dois tipos | é o erro posicional de hoje, só que dito na língua de quem vai consertar |

A lista do `INSERT` é, portanto, a **interseção** — colunas do destino que
`incoming` também tem, na ordem do destino. E a mensagem de erro segue o estilo
que o próprio SDK já usa em `addMetadataToEnvelope` (`load.go:271`): nomeia o
campo em vez de descrever a classe do problema.

### Torne isso testável — hoje não é

O SQL é montado dentro de `loadWithMerge`, que precisa de um cliente BigQuery.
É por isso que nenhum teste unitário viu essa string. Extraia:

```go
// mergeSQL monta o MERGE. Puro: nada de rede, para que o SQL gerado possa
// ser afirmado num teste.
func mergeSQL(dest, temp *bigquery.Table, cols []string, key string) string
```

e uma segunda função pura para a reconciliação:

```go
func reconcile(dest, incoming bigquery.Schema) (cols []string, err error)
```

As duas rodam sob `-short`, que é onde os testes de fato rodam hoje.

### Limpeza no mesmo arquivo

`dedup.go:57-64` diz a mesma coisa duas vezes:

```go
// WHEN NOT MATCHED only. The landing layer is append-only by contract, so
// a row already there is left exactly as it was -- a re-run must never
// rewrite history, only skip it.
...
// WHEN NOT MATCHED only. A row already there is left exactly as it was --
// a re-run must skip history, never rewrite it.
```

Fique com uma. E corrija a afirmação falsa da linha 61 — foi ela que me fez
procurar o defeito no lugar errado primeiro, o que é o custo real de um
comentário errado.

### Como provar

1. Unitário: `mergeSQL` produz lista nomeada, com crase, na ordem do destino.
2. Unitário: `reconcile` devolve erro nomeando a coluna nos dois casos de erro
   da tabela acima, e a interseção no caso bom.
3. Integração novo: destino com as colunas em ordem **diferente** da que o
   autodetect produz para o payload, e a carga passa. Este é o teste que hoje
   falha e que nenhum dos existentes cobre.

---

## 3. O que não fazer

- **Não ordene nem recrie o destino para casar com a temporária.** O SDK não
  altera tabela existente, e isso está escrito como princípio em `table.go:26`
  ("A loader that can ALTER or DROP is a loader that can erase history"). A
  ordem das colunas da landing de um consumidor não é negociável.
- **Não relaxe a exigência de `ExtraMetadata` no `DedupMerge`**
  (`load_test.go:542`): o `MERGE` casa por `ingestion_id`, e sem os metadados
  essa coluna não existe.
- **Não mexa nos itens 3 e 4** do `SDK_V9.md` aqui. O item 4 é decisão de
  produto e vai mudar assinatura; embutir isso neste conserto faz um diff que
  ninguém revisa.

---

## 4. Critério de pronto para a `sdk/v0.10.2`

1. `TestIntegrationMergeDoesNotDouble` roda e passa, **sem ter sido alterado**.
2. O destino criado pelo caminho do dedup tem o mesmo layout do criado pelo
   caminho sem dedup, com teste. Uma única função decide o layout.
3. `409 Already Exists` na criação não derruba a carga.
4. O `MERGE` nomeia as colunas, com crase, na ordem do destino.
5. `mergeSQL` e `reconcile` são puras e testadas sob `-short`.
6. Coluna em `incoming` que o destino não tem é erro nomeando a coluna.
7. Teste de integração com as ordens divergentes.
8. `dedup.go:61` não afirma mais que o BigQuery casa por nome, e o parágrafo
   duplicado saiu.
9. `CHANGELOG.md` com a entrada da `0.10.2`. As entradas pararam na `0.2.1` —
   a `0.9.0` reverteu o contrato de seis colunas sem uma linha de changelog, e é
   por isso que o consumidor descobriu a mudança por um erro de tipo do
   BigQuery em vez de por um `BREAKING CHANGE` escrito.
10. `go test ./sdk/... -short` verde, e `go vet ./sdk/...` limpo.
