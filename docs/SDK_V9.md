# SDK — dois defeitos no `load` da `v0.9.1`, e uma decisão de produto

**Aberto em** 2026-09-03 · **Versões analisadas** `sdk/v0.9.0`, `sdk/v0.9.1`,
`sdk/v0.10.0` · **Alvo** `sdk/v0.10.1`

> **ITENS 1 E 2 CONCLUÍDOS em 2026-09-03, entregues na `sdk/v0.12.0`.** Ver
> [`plan/2026-09-03-sdk-conserto-do-merge.md`](plan/2026-09-03-sdk-conserto-do-merge.md)
> e o `CHANGELOG.md`, que foi reconstruído da `0.3.0` à `0.12.0` no mesmo passo.
>
> Verificado por quem reportou, na `v0.12.1`:
>
> ```
> go vet ./...            limpo
> go test ./... -short    4 pacotes ok
> go test ./load/ -run TestIntegration -v
>   PASS  TestIntegrationMergeDoesNotDouble              (item 1, teste nao alterado)
>   PASS  TestIntegrationMergeIntoADifferentColumnOrder  (item 2, novo)
>   PASS  TestIntegrationFirstMergeLoadStillPartitions   (a costura da v0.11.0)
>   PASS  TestIntegrationInlineStrategy / CreatesTableFromData / RefusesMissingTableUnasked
>   SKIP  TestIntegrationGCSStrategy                     (BRAVIS_IT_BUCKET ausente)
> ```
>
> O item 1 foi resolvido por um caminho **diferente** do que a spec propôs, e
> melhor: em vez de criar o destino a partir do schema da temporária, o
> `DedupMerge` cede ao caminho comum quando a tabela não existe
> (`load.go:241`) — numa primeira carga não há contra o que deduplicar, e o
> caminho comum já é quem cria a tabela com o layout certo. Isso dispensa os
> critérios 2 e 3 da spec por construção: não há um segundo lugar criando
> tabela, então não há decisão de layout duplicada nem corrida de `409`.
>
> **Seguem abertos os itens 3 e 4**, que estavam fora do escopo daquela spec de
> propósito. O item 4 é hoje a **única** razão pela qual o consumidor
> `zarv-data-pipeline` continua na `v0.8.0`.

Achados ao migrar o consumidor `zarv-data-pipeline` da `v0.8.0` para a `v0.9.x`.
A migração foi **revertida**: o consumidor está preso na `v0.8.0` e não sobe
enquanto os itens 1 e 2 estiverem abertos.

Cada item traz o arquivo e a linha, o que o código faz, como reproduzir, e como
provar que ficou certo. O `extract` não é o assunto: subiu sem incidente, e o
`Driver` explícito nas duas pontas (`DriverHTTP`, `DriverBigQuery`) funciona.

> **Nem a `v0.9.1` nem a `v0.10.0` tocam o pacote `load`.** Os diffs mexem só
> em `sdk/types.go` e `sdk/sdk_test.go` (`v0.9.1`) e em `sdk/{runcontext,target,
> pipeline,sdk,types}.go` mais o README (`v0.10.0`). Os três defeitos abaixo
> valem para as três versões, e o item 2 foi reconfirmado rodando a `v0.10.0`
> contra a landing real do consumidor:
>
> ```
> v0.10.0 + ExtraMetadata -> bronze.vendors_open_meteo_hourly_temperatures
> Error 400: Value has type FLOAT64 which cannot be inserted into column
> ingestion_id, which has type STRING
> ```
>
> O que a `v0.10.0` trouxe — `RunContext` e o `CreateTable` tri-estado — é bom e
> não é o assunto aqui, com uma exceção que **agrava o item 1**: ver §1.1.

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

### 1.1 A `v0.10.0` fez este defeito ficar mais fácil de encontrar da pior forma

`CreateTable` passou de `bool` para `*bool` tri-estado, e com `nil` — que é o
valor de quem **não escreveu o campo** — a decisão passa para o engine:

> `nil` you did not say. Inside Bravis the engine decides: it creates on the
> step's first successful run, or when the run was dispatched with
> `create_table=true`.

Antes, cair no item 1 exigia alguém escrever `CreateTable: true`. Agora um
fetcher que **não menciona** `CreateTable` e usa `DedupMerge` tem a criação
ligada pelo engine na primeira execução — e essa primeira execução falha com
404, que é justamente a execução em que ninguém ainda sabe se o pipeline
funciona.

O tri-estado está certo e o raciocínio dele está bem escrito. O problema é que
ele amplia o alcance de um caminho que não cria a tabela.

### Como provar que ficou certo

> **Correção do que eu escrevi aqui na primeira versão.** Eu disse que a
> combinação nunca foi exercitada por nenhum teste. Errado: ela é exatamente o
> que `TestIntegrationMergeDoesNotDouble` (`integration_test.go:191`) monta —
> tabela ausente, `CreateTable(true)`, `ExtraMetadata(true)`,
> `Dedup(DedupMerge)`. O teste **existe e cobre o defeito**.
>
> Ele nunca rodou. `requireIntegration` (`integration_test.go:35`) pula sem
> `BRAVIS_IT_PROJECT`, e essa variável não está definida em lugar nenhum
> automatizado — é a mesma pendência que registrei no cabeçalho do
> [`SDK_LOAD.md`](SDK_LOAD.md) em 2026-09-02.
>
> Isso muda a conclusão: o defeito não escapou por falta de teste, escapou
> porque o teste que o cobre está atrás de uma variável de ambiente. Qualquer
> conserto que não deixe essa suíte rodando em algum lugar automatizado deixa a
> próxima regressão passar igual.

Rodar o que já existe:

```bash
export BRAVIS_IT_PROJECT=<projeto-gcp>
go test ./sdk/load/ -run TestIntegrationMergeDoesNotDouble -v
```

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

## 3. `msg=loaded` sai antes de saber se carregou — **ABERTO**, e localizado

Numa carga que falhou, a saída foi:

```
msg=loaded  pipeline=open_meteo/hourly_temperature records=24 ...
msg=failed  error="target bronze.… refused: waiting for merge: …404…"
```

Reconfirmado na `v0.12.1`, e agora com a linha exata. `pipeline.go:145`:

```go
res, err := loadWith(ctx, data, p.Target, p.Run)
if res != nil {
    slog.Info("loaded", append([]any{"pipeline", p.name()}, res.Args()...)...)
    ...
}
return err
```

O `slog.Info("loaded", ...)` dispara sempre que existe resultado, e existe
resultado em todo caminho de erro — por desenho, desde a `v0.2.1`, para que
`result.ErrorRows` seja legível depois de uma falha. As duas decisões estão
certas isoladas; juntas produzem `msg=loaded` numa carga que não carregou.

Repare que o próprio log carrega a verdade ao lado da mentira:
`records=24 lines=0`. Quem filtrar por `msg=loaded` conta 24; quem ler
`lines` vê 0.

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

## 4. A decisão de produto: quem produz as seis colunas — **ABERTO, e agora é o único bloqueio**

> **Correção do que eu mesmo escrevi na primeira versão deste documento.** Eu
> descrevi o modo envelope como opt-in. Ele era o **padrão**, opt-**out**:
>
> ```go
> // v0.8.0, sdk/target.go:127
> WriteEnvelopeColumns: !d.RawPayload,
> ```
>
> A regressão é maior do que registrei: as seis colunas não deixaram de ser
> pedíveis, deixaram de ser **alcançáveis** — não há combinação de campos na
> `v0.10.0` que as produza.

A `v0.9.0` removeu `WriteEnvelopeColumns` / `WithEnvelopeColumns` e o trocou por
`ExtraMetadata`:

```
v0.8.0   sdk/target.go:127  WriteEnvelopeColumns: !d.RawPayload   <- padrao
v0.10.0  sdk/target.go:108  ExtraMetadata bool                    <- dois campos
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

Reproduzir o contrato em cima da `v0.10.0` não é só trabalhoso, é instável.
Aninhar o payload e acrescentar `provider` e `entity` num `Compute` é fácil;
`source_key` exigiria recalcular a lógica do `Key`, e uma diferença de
formatação de float faria o `ingestion_id` ser calculado a partir de um
`source_key` diferente do que a coluna declara.

E mesmo pagando esse preço, **o item 2 ainda bloqueia**: a ordem das colunas da
temporária vem do autodetect, e o `INSERT ROW` é posicional. Não há como um
consumidor com landing de schema fixo garantir a correspondência.

Por isso o consumidor está preso na `v0.8.0` por causa do **item 2**, não do
item 4. O item 4 é a decisão de produto; o item 2 é o que impede a migração
mesmo se a decisão for "aceite o payload plano".

**Recomendação: devolver o modo envelope opt-in**, convivendo com
`ExtraMetadata`. Se a decisão for mantê-lo fora, ela precisa estar escrita com o
custo assumido, porque o consumidor terá de escolher entre divergir dos 24
vendors em Python ou duplicar a chave.

---

## 5. Critério de pronto para a `v0.10.1`

> Os itens 1 e 2 têm spec de execução própria, com implementação e provas:
> [`plan/2026-09-03-sdk-conserto-do-merge.md`](plan/2026-09-03-sdk-conserto-do-merge.md).

1. `CreateTable` + `DedupMerge` cria o destino, com teste de integração na
   combinação.
2. O `MERGE` nomeia as colunas, com teste em que as ordens divergem. Comentário
   de `dedup.go:60` corrigido.
3. `msg=loaded` só sai depois de a carga ter acontecido.
4. Chaves de log num único idioma.
5. Decidido o §4, e escrito onde a decisão fica visível para quem consome.
