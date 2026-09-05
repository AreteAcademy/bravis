# SDK — dois defeitos no `load` da `v0.9.1`, e uma decisão de produto

> **HISTÓRICO — não é o estado atual.** Este documento vale para `sdk/v0.9.x`.
> É um relatório do consumidor sobre a `v0.9.x`. Os dois defeitos que ele levanta foram corrigidos na `v0.12.0`.
>
> Para o SDK como ele é hoje: [`SDK_ARQUITETURA.md`](SDK_ARQUITETURA.md),
> [`SDK_NOVO_DRIVER.md`](SDK_NOVO_DRIVER.md) e [`SDK_DECISOES.md`](SDK_DECISOES.md).

> **OS OITO PRIMEIROS ITENS ESTÃO RESOLVIDOS**, o último na `sdk/v0.24.0`.
> O **§9 foi aberto depois**, na `v0.27.2`, e é da mesma classe de "telemetria que
> mente": a renovação de credencial não renova nada quando a URL dela não
> compartilha o prefixo de path com a da fonte — e falha em silêncio. O que os oito custaram e as classes que se repetiram estão em
> [`SDK_CONSUMIDOR.md`](SDK_CONSUMIDOR.md).

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
>   SKIP  TestIntegrationGCSStrategy                     (BREVIS_IT_BUCKET ausente)
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
> **O item 4 foi resolvido na `sdk/v0.15.0`**, por um caminho melhor que o
> proposto: `sdk.Schema(...)` como Transformer e um bloco `Metadata` que nomeia
> as duas colunas no ponto de chamada — sem DSL nova, reusando o Transform. O
> consumidor migrou da `v0.8.0` direto para a `v0.15.0` e compõe as seis colunas
> explicitamente. Os `ingestion_id` bateram com os das cargas anteriores, então a
> troca não reingeriu nada.
>
> **Seguem abertos o item 3** (`msg=loaded` antes da falha) **e o item 6**
> (a temporária do merge por autodetect), este último achado na migração e hoje
> o único motivo de o consumidor rodar sem `DedupMerge`.

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

> `nil` you did not say. Inside Brevis the engine decides: it creates on the
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
> `BREVIS_IT_PROJECT`, e essa variável não está definida em lugar nenhum
> automatizado — é a mesma pendência que registrei no cabeçalho do
> [`SDK_LOAD.md`](SDK_LOAD.md) em 2026-09-02.
>
> Isso muda a conclusão: o defeito não escapou por falta de teste, escapou
> porque o teste que o cobre está atrás de uma variável de ambiente. Qualquer
> conserto que não deixe essa suíte rodando em algum lugar automatizado deixa a
> próxima regressão passar igual.

Rodar o que já existe:

```bash
export BREVIS_IT_PROJECT=<projeto-gcp>
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

## 3. `msg=loaded` sai antes de saber se carregou — **RESOLVIDO**

> Conserto: `pipeline.go:167` passou a ramificar — `slog.Error("load failed")`
> quando há erro, `slog.Info("loaded")` quando não. O resultado continua vindo
> no caminho de falha, por desenho, para que `RowErrors` seja legível; o que
> mudou é a mensagem saber distinguir os dois. O comentário no código diz o
> porquê melhor do que este relatório dizia: *"loaded num load que não escreveu
> nada é uma linha que alguém vai grepar e acreditar, e em INFO ela nem chega a
> quem observa erro"*.

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

## 4. A decisão de produto: quem produz as seis colunas — **RESOLVIDO na v0.15.0**

> Conserto: `sdk.Schema` (hoje `sdk.Accept`) como Transformer e o bloco
> `Metadata` nomeando as duas colunas no ponto de chamada; a declaração completa
> chegou na `v0.18.0` como `Target.Columns`. O consumidor compõe as seis colunas
> explicitamente, e os `ingestion_id` bateram com os das cargas anteriores.

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

## 5. `Source.Preview` amostra do lado errado do `Expand` — **RESOLVIDO na v0.19.0**

> Resolvido de lado: ao mover o `Records` para dentro do driver, o fatiamento
> passou a acontecer **dentro** do `extract` (`from/http.go:72` →
> `extract.JSON(ctx, source, h.Records)`), e o embrulho do preview
> (`extract/extract.go:131`) passou a ver os registros já fatiados.
>
> Verificado no consumidor: o rodapé agora diz `3 of 24 rows` e a linha seguinte
> diz `24 records`. Os dois números concordam, o que era o defeito.

O preview da `v0.13.0` é útil e o desenho está certo em quase tudo: a amostra é
colhida enquanto os registros passam, sai também quando a fonte morre no meio, e
não passa por `slog` porque o `TextHandler` escaparia as quebras de linha. Mas
ele mostra o payload **antes** do `Expand`.

`extract/extract.go:121` embrulha o `yield` e amostra `env.Payload`. O `Expand`
é aplicado em `sdk.go:111`, **fora** do pacote `extract` — então para qualquer
fetcher com `Expand`, o preview mostra o envelope cru e não os registros.

No consumidor, `Source.Expand = ParallelArrays("hourly", "time",
"temperature_2m")`:

```
msg="extract complete" pages=1 rows=1 bytes=846 per_page=51µs

   elevation  generationtime_ms  hourly                                    hourly_units       latitude    longitude
0        737  0.0983476638793945  {"temperature_2m":[14.1,13.6,13.2,12.6,…  {"temperature_2m…  -23.514938  -46.610504

[1 row · 9 columns (2 not shown) · 846 B · 1 page in 51µs]
dry-run open_meteo/hourly_temperature -> ... (24 records, 1 page(s), 1 attempt(s), 781ms)
```

`1 row` no rodapé, `24 records` na linha seguinte, para o mesmo extract. E as
colunas exibidas — `elevation`, `hourly_units`, `generationtime_ms` — não são as
que chegam à tabela; `generationtime_ms` é justamente o campo que o consumidor
remove no `Transform` porque muda a cada chamada.

O caso em que "o que eu puxei, afinal?" mais importa é exatamente o do `Expand`:
uma resposta com arrays paralelos ou coleção aninhada é onde ninguém consegue
prever a forma do registro de cabeça. É onde o preview mostra menos.

**O conserto** é amostrar onde os registros do consumidor existem: mover a
amostragem e a renderização para o embrulho de expansão do `sdk.Extract`, ou
passar um tap pós-`Expand` para o `extract`. `Stats.Bytes` deve continuar
medindo o fio — 846 B está certo e é útil.

**Como provar:** um teste com `Expand` definido, afirmando que o preview traz os
registros expandidos e que o rodapé conta o mesmo que o consumidor recebe. Todo
teste de preview de hoje roda sem `Expand`, e é por isso que isto passou.

Menor, no mesmo caminho: `rows` do `extract complete` conta o que o pacote
`extract` emitiu, não o que o consumidor recebe. Ou os dois números viram um, ou
o nome diz qual dos dois é.

---

## 6. A temporária do merge nasce por autodetect, e não cabe no destino — **RESOLVIDO na v0.23.0**

> Conserto: a encenação passa a tomar o schema do destino, que `prepareTable` já
> leu. `AutoDetect` sai do caminho do dedup. Teste de integração
> `TestIntegrationMergeIntoAJSONColumn`, verificado a falhar sem o conserto com o
> erro exato reportado aqui, e a suíte inteira (14 testes) passando com ele.

Achado migrando o consumidor para a `v0.15.0`. É o único bloqueio que sobrou, e
é pequeno.

`dedup.go` cria a temporária sem schema e carrega com `source.AutoDetect = true`.
O autodetect transforma um objeto JSON aninhado em `RECORD`. Um destino que
declara essa coluna como `JSON` — que é o tipo certo para payload de vendor, e o
que as 32 landings deste consumidor usam — não recebe:

```
type mismatch on payload (destination JSON, incoming RECORD)
```

A mensagem está **certa**: é a `reconcile` fazendo o trabalho dela, e nomeando os
dois tipos. O problema é que a temporária foi montada por inferência quando havia
uma fonte de verdade melhor à mão.

### Isolado nas duas direções

| configuração | resultado |
|---|---|
| `Dedup: DedupMerge` | `type mismatch on payload (destination JSON, incoming RECORD)` |
| sem `Dedup` | `rows=24 ignored=0`, e `JSON_VALUE(payload, '$.temperature_2m')` lê no destino |

Mesmo fetcher, mesma linha, mesmo destino. Só o caminho muda.

### O conserto

A temporária existe para receber exatamente as linhas que vão ao destino, e o
destino **já existe** nesse ponto — `prepareTable` acabou de lê-lo. Crie a
temporária com o schema do destino em vez de autodetect:

```go
destMeta, err := table.Metadata(ctx)          // ja lido em prepareTable; passe-o
temp.Create(ctx, &bigquery.TableMetadata{
    Schema:         destMeta.Schema,
    ExpirationTime: time.Now().Add(6 * time.Hour),
})
```

Isso resolve mais do que o tipo. Com o schema do destino, a ordem das colunas da
temporária deixa de ser inferida — o que remove pela raiz a classe do §2, em vez
de depender da lista nomeada para compensá-la. E a `reconcile` passa a comparar
o que as linhas trazem com o destino, que é a comparação que interessa, em vez
de comparar destino com uma inferência.

### Como provar

Um teste de integração com uma coluna `JSON` no destino e um objeto aninhado no
payload, com `DedupMerge` ligado. Hoje ele falha. Os testes de merge existentes
usam colunas escalares, e é por isso que isto passou.

---

## 7. `FormatError.Format` é declarado e nunca preenchido — **RESOLVIDO na v0.23.0**

> Conserto: o campo foi **removido**, não preenchido — o formato do fio não é
> alcançável nos quatro sites, e três deles não são sobre ele. A mensagem passou
> para inglês, e `errors_test.go` afirma a string, com checagem de espaço duplo.

`errors.go:54` declara o campo e `errors.go:61-63` o imprime:

```go
return fmt.Sprintf("formato %s from %s: %v", e.Format, e.URL, e.Cause)
```

**Nenhum dos quatro sites que constroem o erro preenche `Format`** —
`sdk.go:199`, `sdk.go:207`, `sdk.go:249` e `transform.go:79` passam `URL`,
`Line` e `Cause` e nada mais. Conferido com
`grep -A4 'FormatError{' | grep 'Format:'`: vazio.

O resultado é que todo erro de formato sai com um buraco no lugar do formato:

```
error="formato  from http://…/erro200: open-meteo recusou: parametro invalido"
        ^^^^^^^^ dois espaços
```

E a mesma execução loga `format=json` no `extract complete`, porque ali o valor
vem do `core.Source` já normalizado (`from/http.go:70-71`). Um run diz duas
coisas sobre o mesmo formato: `json` no sucesso, nada no erro.

É a mesma família de `LoadResult.ErrorRows` (declarado, nunca preenchido, e o
README mandava lê-lo), do `CursorKey`/`PageSize`/`HasHeader` da `v0.1.1`, e do
`DeleteAfterLoad` documentado como `default: true` que um `bool` não conseguia
ser. **Campo público que não faz nada é pior que campo ausente**, e aqui ele
chega ao operador de plantão como uma frase truncada.

Menor, no mesmo lugar: a mensagem mistura idiomas — `formato %s from %s,
registro %d`.

**Conserto:** preencher nos quatro sites a partir do formato **normalizado**, o
mesmo que o `extract complete` reporta — ou remover o campo e a interpolação.
Qualquer um dos dois é honesto; o estado atual não.

**Como provar:** um teste que afirma a string do `Error()` de cada um dos quatro
sites. Hoje nenhum teste olha a mensagem, e é por isso que dois espaços passaram.

---

## 8. `Metadata` é documentado como interruptor, e não é um — **RESOLVIDO na v0.24.0**

> Resolvido pela raiz, e não pelo texto: o bloco `Metadata` **deixou de
> existir**. As duas colunas viraram `sdk.IngestionID()` e
> `sdk.IngestionLoadedAt()`, transformers como quaisquer outros. Não há mais
> interruptor a descrever, nem três estados a explicar.

`metadata.go:11`:

> It is a switch for those two columns, not a place to put data.

**Um interruptor com quatro campos obrigatórios não é um interruptor.** Esta
frase custou duas rodadas de perguntas de quem consome o SDK — a mesma pergunta,
duas vezes, depois de uma resposta que repetia a frase:

> "Metadado deve ser ligado ou desligado?"

A pergunta não tem resposta porque a premissa está errada. O bloco é a **receita
do `ingestion_id`**: os quatro campos são os quatro componentes de
`uuid_v5(ns, "provider|entity|source_key|record_ts")` (`core/types.go:46`).
Declarar a receita é o que implica as duas colunas — mas o que se declara é a
receita, não um interruptor.

E não são dois estados, são **três**, cada um com uma consequência diferente para
o `DedupMerge`:

| bloco | colunas | `ingestion_id` | `DedupMerge` |
|---|---|---|---|
| ausente | nenhuma | — | recusado (`load.go:119`) |
| `&Metadata{AutoID: true}` | as duas | aleatório | recusado (`load.go:113`) |
| os quatro campos | as duas | determinístico | possível |

A parte da frase que **acerta** é "not a place to put data": nada escrito no
bloco vira coluna, e vale dizer isso — o consumidor tem `Provider` e `Entity`
duas vezes no fetcher, uma aqui para o id e outra no `Transform` para a coluna, e
sem essa frase pareceria duplicação.

**O conserto é de texto, e é pequeno:** trocar "switch" por o que o bloco é, e
listar os três estados com o que cada um faz ao `DedupMerge`. Os dois erros de
`load.go:113` e `:119` já nomeiam o problema em runtime; o que falta é a
documentação dizer o mesmo antes.

**Como provar:** dar o bloco a alguém que nunca viu o SDK e pedir para explicar
o que ele faz. Se a pessoa perguntar "isto liga ou desliga alguma coisa?", o
texto ainda não está certo.

---

## 9. A renovação de credencial vai sem a credencial, e a execução morre — **RESOLVIDO na v0.27.3**

`Auth.Refresh` existe para empurrar a janela de uma sessão que expira. No
consumidor ele **não empurra nada**, e não avisa que não empurrou.

### Reproduzido, com a causa isolada

Servidor local, imprimindo os cabeçalhos que chegam. Fetcher com
`Apply: AsCookie` e `Value: FromEnv(...)`:

| URL da fonte | URL da renovação | a renovação recebeu `Cookie`? |
|---|---|---|
| `/api/proxy/occurrences` | `/api/auth/session` | **não** |
| `/api/proxy/occurrences` | `/api/proxy/session` | sim |

A diferença é só o **prefixo de path**.

### Por quê

`AsCookie` põe a credencial no jar, semeada a partir da **URL da fonte**. O
`cookiejar` do Go, quando o cookie não traz `Path`, usa como padrão o
**diretório da URL que o originou** — aqui, `/api/proxy`. A renovação em
`/api/auth/session` não casa com esse prefixo, e o jar não a envia.

### Não é um aviso mudo: é a execução inteira

Corrigindo o que este relatório dizia na primeira versão. Eu descrevi o efeito
como silencioso — a carga funcionaria e só a renovação ficaria inerte. **Está
errado, e o consumidor descobriu em produção:**

```
step "fetch_occurrences": saiu com codigo 1
error="format error in …/occurrences: refresh …/auth/session:
       refresh response has no field \"expires\""
```

A cadeia é: a renovação vai sem credencial → o endpoint responde `null` para não
autenticado → `ExpiresAt` não acha `expires` → **erro, e a execução morre antes
da primeira página.**

Verificado que `null` é mesmo a resposta de não autenticado, com e sem cookie:

```
/auth/session COM cookie válido    -> 200, {"user":{…},"expires":"…"}
/auth/session sem credencial       -> 200, null
```

Então **um `Refresh` com `ExpiresAt` é hoje inutilizável** para qualquer fonte
cuja URL de renovação não compartilhe o prefixo de path com a dos dados — que é
a disposição normal (`/api/proxy/dados` e `/api/auth/session`).

O consumidor teve de remover o bloco. Com isso perde a renovação **e** o aviso —
volta a ter uma credencial que morre calada no prazo de quem a colou, que era o
problema que a `v0.27.0` foi escrita para resolver.

Vale registrar o lado bom: o `ExpiresAt` **falhar** em vez de seguir sem data é o
que tornou o defeito visível. Se ele tivesse tratado a ausência como "sem
validade conhecida", isto teria ficado invisível até o dia 31.

### Por que o teste do SDK não pega

`TestRefreshRenovaOCookieParaAsPaginas` usa `srv.URL + "/dados"` como fonte. O
diretório dessa URL é `/`, que casa com qualquer path — inclusive
`/auth/session`. **O teste passa porque a fonte está na raiz**, e nenhuma API de
verdade está.

### O conserto

Semear o jar com `Path=/`, ou aplicar a credencial no header da requisição de
renovação em vez de depender do jar. A segunda é mais direta e não muda o
comportamento das páginas.

**Como provar:** o teste existente, com a fonte em `/api/v1/dados` e a renovação
em `/auth/session`. Hoje ele falha.

### O que foi feito, na `v0.27.3`

Nenhuma das duas opções isoladamente bastava, e a razão só apareceu ao escrever
o teste. Semear o jar com `Path=/` conserta a ida — a renovação passa a receber
a credencial. Mas o cookie que ela **reemite** volta a ficar preso, agora em
`/api/auth`, e as páginas seguem com o valor velho: a renovação renova para
ninguém, e o defeito reaparece na direção oposta.

Então a credencial **deixou de ser cookie de jar** e passou a ser cabeçalho, que
vale para toda requisição independentemente de path. O jar continua existindo
para os demais cookies, e um `credentialJar` desvia os nomes da credencial antes
que ele os guarde — o que mantém a invariante da `v0.26.0` (cada cookie mora num
lugar só, nenhum nome vai duas vezes) e dá de brinde o que a spec do volume
precisa: **o valor rotacionado fica na mão**, em vez de enterrado no jar.

A rotação é aplicada também no laço de páginas, e não só após a renovação: uma
API pode reemitir a sessão em qualquer resposta, e antes o jar absorvia isso
sozinho. Sem essa linha, a página 2 iria com o valor que a página 1 acabou de
substituir — regressão que o teste da `v0.26.0` pegou.

O teste novo cobre as quatro disposições de path, e as três reversões (apagar o
`Cookie` da renovação, não aplicar a rotação após ela, não aplicá-la no laço)
derrubam cada uma o seu.

> Não consegui conferir contra a API real do fornecedor: a credencial vivia numa
> tabela que foi removida — corretamente, por ser o lugar errado — e ainda não
> foi recolocada como variável de ambiente. O que está acima é do servidor local,
> reproduzido nas duas direções.

---

## 10. Critério de pronto para a `v0.10.1`

> Os itens 1 e 2 têm spec de execução própria, com implementação e provas:
> [`plan/2026-09-03-sdk-conserto-do-merge.md`](plan/2026-09-03-sdk-conserto-do-merge.md).

1. `CreateTable` + `DedupMerge` cria o destino, com teste de integração na
   combinação.
2. O `MERGE` nomeia as colunas, com teste em que as ordens divergem. Comentário
   de `dedup.go:60` corrigido.
3. `msg=loaded` só sai depois de a carga ter acontecido.
4. Chaves de log num único idioma.
5. Decidido o §4, e escrito onde a decisão fica visível para quem consome.
