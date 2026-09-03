# SDK — dois defeitos no `load` da `v0.9.1`, e uma decisão de produto

**Aberto em** 2026-09-03 · **Versões analisadas** `sdk/v0.9.0`, `sdk/v0.9.1` ·
**Alvo** `sdk/v0.9.2`

Achados ao migrar o consumidor `zarv-data-pipeline` da `v0.8.0` para a `v0.9.x`.
A migração foi **revertida**: o consumidor está preso na `v0.8.0` e não sobe
enquanto os itens 1 e 2 estiverem abertos.

Cada item traz o arquivo e a linha, o que o código faz, como reproduzir, e como
provar que ficou certo. O `extract` não é o assunto: subiu sem incidente, e o
`Driver` explícito nas duas pontas (`DriverHTTP`, `DriverBigQuery`) funciona.

> A `v0.9.1` **não** toca o pacote `load` — `git diff sdk/v0.9.0 sdk/v0.9.1`
> mexe só em `sdk/types.go` e `sdk/sdk_test.go`. Os dois defeitos abaixo valem
> para as duas versões.

---

## 1. `CreateTable` não cria a tabela quando `DedupMerge` está ligado — **bloqueante**

`CreateTable: true` mais `Dedup: DedupMerge` numa tabela ausente falha com 404,
e a tabela **nunca é criada**.

```
target staging.exemplo_v9 refused: waiting for merge:
googleapi: Error 404: Not found: Table zarv-development-94b6:staging.exemplo_v9
was not found in location US
```

### Por que

`load.go:202` chama `prepareTable`, que em `table.go:44` devolve
`(false, nil)` quando a tabela falta e `CreateSQL` está vazio — delegando a
criação ao load job, com o comentário certo:

```go
if l.cfg.CreateSQL == "" {
    // The load job creates it, inferring the schema from the data.
    return false, nil
}
```

Só que quem materializa essa promessa é `applyLayout`, e ele é chamado em
exatamente dois lugares:

```
sdk/load/load.go:419   loadInline
sdk/load/load.go:457   loadViaGCS
```

O ramo do `DedupMerge` (`load.go:214`) não passa por nenhum dos dois. Em
`dedup.go:50` o load job escreve na **temporária**, e a temporária é criada à
mão em `dedup.go:34`. O destino nunca recebe `CreateDisposition:
CreateIfNeeded`, então o `MERGE` de `dedup.go:65` é o primeiro a tocá-lo — e
morre no 404.

Depois, `load.go:241` ainda reporta `TableCreated` com base no `existed` que
`prepareTable` devolveu, não no que aconteceu.

Há um teste em `load_test.go:653` que trava `applyLayout` ao job justamente
"porque foi escrito e não era chamado". Ele cobre os dois caminhos sem dedup e
não cobre o terceiro.

### Reproduzido, nas três direções

| configuração | resultado |
|---|---|
| `CreateTable`, sem dedup, tabela ausente | `rows=24 created=true` OK |
| `CreateTable` + `DedupMerge`, tabela ausente | **404, tabela não existe depois** FALHA |
| `DedupMerge`, tabela já existente | `rows=0 ignored=24` OK |

`INFORMATION_SCHEMA.COLUMNS` confirma a ausência no caso do meio.

### Como provar que ficou certo

Um teste de integração que carrega com `CreateTable` **e** `DedupMerge` numa
tabela que não existe, e depois faz `SELECT COUNT(*)`. Os dois testes de
integração de hoje (`integration_test.go:210` e `:268`) usam `CreateTable` sem
dedup — a combinação nunca foi exercitada, e é por isso que ela passou.

O conserto natural é o `MERGE` deixar de ser a primeira escrita: criar o destino
a partir do schema da temporária quando `CreateTable` estiver ligado e a tabela
faltar. A temporária já tem o schema certo nesse ponto, vindo do autodetect.

---

## 2. O `MERGE` usa `INSERT ROW`, que casa por POSIÇÃO — e o comentário afirma o contrário

**`dedup.go:60`**

```go
// INSERT ROW rather than a column list: the SDK does not know your
// payload, and BigQuery matches the columns by name.
```

**O BigQuery casa por posição.** Provado direto, com duas tabelas de nomes
idênticos e ordem trocada:

```sql
CREATE TABLE staging._t_alvo  (a STRING, b INT64);
CREATE TABLE staging._t_ordem (b INT64, a STRING);
INSERT staging._t_ordem VALUES (7, 'sete');

MERGE staging._t_alvo t USING staging._t_ordem i
  ON t.a = i.a
  WHEN NOT MATCHED THEN INSERT ROW;
```
```
Value has type INT64 which cannot be inserted into column a,
which has type STRING
```

Se casasse por nome, isso passaria.

### Por que isso é grave aqui

O schema da temporária vem do **autodetect** sobre o payload que o chamador
devolveu do `Transform` (`dedup.go:48`, `source.AutoDetect = true`). A ordem das
colunas da temporária, portanto, não está sob controle de quem chama — e o
`INSERT ROW` só acerta quando ela coincide com a ordem do destino.

No consumidor, com `ExtraMetadata: true` contra uma landing de schema fixo:

```
Value has type FLOAT64 which cannot be inserted into column
ingestion_id, which has type STRING at [5:30]
```

`latitude` caiu em `ingestion_id`. O destino é
`(ingestion_id, ingestion_loaded_at, provider, entity, source_key, payload)`,
conferido em `INFORMATION_SCHEMA`.

> **Não investiguei** que ordem o autodetect do BigQuery produz a partir de
> NDJSON, e por isso não afirmo o mecanismo. O que está provado é o que importa
> para o conserto: `INSERT ROW` é posicional, e a posição vem do autodetect.
>
> Vale registrar que na `v0.8.0` o mesmo caminho **funciona**, com inserção real
> (`rows=24 ignored=0`) num destino cuja ordem não é alfabética. Então o acerto
> de hoje é coincidência de ordem, não garantia — o que é exatamente o problema.

### Como provar que ficou certo

Um teste com destino e payload em **ordens diferentes**, que hoje falha. O
conserto é nomear as colunas em vez de usar `INSERT ROW`: elas são conhecidas em
runtime, pelo schema da temporária.

E corrigir o comentário — ele afirma uma garantia que o BigQuery não dá, e foi
ele que me fez procurar o defeito no lugar errado primeiro.

---

## 3. `msg=loaded` sai antes de saber se carregou — terceira ocorrência da mesma classe

Numa carga que falhou, a saída foi:

```
msg=loaded  pipeline=open_meteo/hourly_temperature records=24 ...
msg=failed  error="target bronze.… refused: waiting for merge: …404…"
```

Nada carregou. É a mesma classe do item 3 do
[`SDK_LOAD.md`](SDK_LOAD.md) (`Format` reportando Parquet enquanto gravava
NDJSON) e do `ErrorRows` que nunca era preenchido: **telemetria que afirma
sucesso num caminho de erro**. Número errado é pior que número ausente, porque
ninguém desconfia dele.

Menor, no mesmo log: as chaves misturam idiomas —
`paginas`, `estrategia`, `formato`, `tabela_criada`, `duracao` ao lado de
`records`, `lines`, `attempts`, `table`, `rows`, `ignored`, `bytes`, `dedup`,
`created`, `duration`. Quem for filtrar log estruturado precisa saber de cor
qual chave está em qual língua.

---

## 4. A decisão de produto: quem produz as seis colunas

A `v0.9.0` removeu `WriteEnvelopeColumns` / `WithEnvelopeColumns` e o trocou por
`ExtraMetadata`:

```
v0.8.0  sdk/types.go:86   WithEnvelopeColumns = core.WithEnvelopeColumns
v0.9.1  sdk/types.go:85   WithExtraMetadata   = core.WithExtraMetadata
```

A documentação nova diz:

> The SDK writes your payload as Transform left it and adds nothing: what a row
> looks like is your decision, not the library's.

`ExtraMetadata` acrescenta **dois** campos (`ingestion_id`,
`ingestion_loaded_at`) ao payload plano. Não produz as seis colunas.

**Esta é a terceira vez que a resposta muda:** agnóstico na `v0.1.1`, contrato
opt-in na `v0.2.1` (pedido pelo §5 do `SDK_LOAD.md`, cuja recomendação era
exatamente "modo envelope opt-in"), agnóstico de novo na `v0.9.0`.

O argumento do §5 continua de pé, e agora com um consumidor real em cima dele: o
`ingestion_id` só evita duplicação se **um** lugar o produz. O
`zarv-data-pipeline` tem 24 vendors em Python que leem a landing pela macro
`metadata_vendor()`, que lê `provider`, `entity` e `payload` **como colunas**.

Reproduzir o contrato em cima da `v0.9.x` exigiria recalcular o `source_key` num
`Compute`, duplicando a lógica do `Key` — a fragilidade que o contrato existia
para evitar.

**Recomendação: devolver o modo envelope opt-in**, convivendo com
`ExtraMetadata`. Se a decisão for mantê-lo fora, ela precisa estar escrita com o
custo assumido, porque o consumidor terá de escolher entre divergir dos 24
vendors em Python ou duplicar a chave.

---

## 5. Critério de pronto para a `v0.9.2`

1. `CreateTable` + `DedupMerge` cria o destino, com teste de integração na
   combinação.
2. O `MERGE` nomeia as colunas, com teste em que as ordens divergem. Comentário
   de `dedup.go:60` corrigido.
3. `msg=loaded` só sai depois de a carga ter acontecido.
4. Chaves de log num único idioma.
5. Decidido o §4, e escrito onde a decisão fica visível para quem consome.
