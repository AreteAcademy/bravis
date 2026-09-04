# Changelog

Versões do SDK (`github.com/AreteAcademy/bravis/sdk`). O formato segue
[Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/), e as versões seguem
[SemVer](https://semver.org/lang/pt-BR/).

A tag de um módulo aninhado leva o prefixo do diretório: `sdk/v0.2.1`.

---

## [0.22.0] — 2026-09-04

HTTP e BigQuery fechados. Nenhuma opção declarada nos dois drivers segue sem
prova contra o serviço de verdade.

### Corrigido

- **A redação da URL deixava passar 8 de 10 segredos.** Ela aparece em **toda**
  linha de log do extract e em **toda** mensagem de erro, e casava só
  `key`, `api_key`, `token`, `auth` e `password` em minúsculas exatas. Passavam
  `API_KEY`, `apikey`, `access_token`, `client_secret`, `secret`, `signature`,
  `sig` — e a pior de todas, a senha em `https://usuario:SENHA@host`, que o
  `url.String` imprime inteira.

  Agora a comparação dobra a caixa, remove separadores e procura marcador por
  substring, e o userinfo é redigido. **Erra para o lado seguro**: um parâmetro
  chamado `monkey` contém `key` e sai redigido — um log escondendo algo inócuo
  não custa nada, e o outro erro põe credencial viva num agregador de logs.
  Tinha zero testes; agora tem cinco.

- **A proveniência tinha parado de rotular a tabela criada.** `Provider` e
  `Entity` alimentam os labels de atribuição de custo e a descrição
  ("quem escreve aqui?"), e a fase 0 parou de repassá-los — toda tabela criada
  desde a `v0.19.0` saiu sem eles. Nada quebrou, nenhuma contagem mudou.

  Consertado onde o godoc do `LoadConfig` sempre prometeu: **vêm do lote**, não
  da configuração. Não há segundo lugar para o fetcher declará-los, e um
  segundo lugar seria uma segunda chance de os dois discordarem.

### Adicionado

- **`bigquery.Table.StagingPrefix`**, `WithStagingPrefix` e
  `WithKeepStagedFile` — três ajustes que existiam no `LoadConfig` e que a
  fachada não alcançava.
- **Teste de completude do adaptador**: lê os campos do `LoadConfig` e os que o
  `bigquery.Table` escreve, e falha quando um campo novo não é ligado nem
  declarado inaplicável. É como a regressão dos labels teria sido pega.
- **Cinco testes de integração para opções nunca provadas contra o BigQuery**:
  `CreateSQL` (com uma coluna `NUMERIC`, que o autodetect jamais produziria),
  `PartitionExpiration` e `RequirePartitionFilter` (incluindo a prova de que
  uma consulta sem filtro é recusada — sem isso só se provaria que uma flag foi
  copiada), `KeepStagedFile` nas duas pontas, e `InlineLimit` decidindo entre
  inline e GCS.
- **`from.HTTP` ganhou arquivo de teste próprio.** Ele é um adaptador, e um
  campo esquecido na cópia para a `core.Source` não quebra nada que compile —
  simplesmente deixa de ter efeito. O teste confere que cada campo chega ao fio.
- `Method`, `Body` e `TotalTimeout` tinham **zero testes**: um fetcher de API
  que exige POST nunca tinha sido exercitado.
- **Testes diretos para `StampMetadata`**, a função que decide o `ingestion_id`
  e que só era exercitada de lado: determinismo, não mutar o lote do chamador,
  recusar nome já ocupado, `AutoID` por linha, e recusar sem identidade. Mais a
  precedência de configuração e o nível de log inválido, que não pode derrubar
  uma pipeline.
- **[`docs/SDK_MATRIZ.md`](docs/SDK_MATRIZ.md)** — o que cada driver suporta,
  as dez combinações recusadas com o motivo, e uma seção do que **ainda não é
  verdade**, para não ser descoberto em produção.

### Números
236 testes no módulo, e **80,0% de cobertura** com os de integração ligados —
medido sobre `extract`, `from`, `to/...`, `load` e `internal/core`. O que puxa
para baixo é `store/s3` e `store/gcs`, que não têm teste unitário: são provados
contra MinIO e contra o bucket real, que é onde um cliente de nuvem se prova.

---

## [0.21.1] — 2026-09-04

### Corrigido
- **O mínimo do Go tinha subido de 1.23 para 1.24, em silêncio.** O `go get` do
  AWS SDK na `v0.20.0` levou o `go.mod` junto, enquanto o README seguia
  prometendo 1.23 — e a `v0.4.x` baixou esse piso de propósito, porque
  "restringia quem podia consumir sem dar nada em troca".

  As dependências da AWS foram fixadas em versões que aceitam 1.23
  (`service/s3 v1.79.0`, `aws-sdk-go-v2 v1.36.3`, `smithy-go v1.22.2`), e o
  piso voltou. Achado pela CI, que confere `go mod tidy` — os testes e o lint
  passavam.

---

## [0.21.0] — 2026-09-04

**BREAKING**, e é o conserto de um defeito que a `v0.20.0` publicou.

### Corrigido
- **`to.BigQuery` e `to.Files` viviam no mesmo pacote**, então escrever um
  arquivo compilava o cliente do Google: **461 pacotes e 21 MB** onde deviam
  ser 195. Achado por um consumidor de fora, medindo o binário — que é
  exatamente a quarta pergunta da prova do §8 do plano dos drivers.

  O teste de poda não pegou porque só cobria o lado `from`, onde HTTP e Files
  são ambos de biblioteca padrão. O buraco era do teste.

### Alterado
- **`to.BigQuery` vira `bigquery.Table`, em `sdk/to/bigquery`.** O campo
  `Table` vira `Name`, porque `Table.Table` não se lê.

  ```go
  // antes
  To: to.BigQuery{Dataset: "bronze", Table: "pedidos"}

  // depois
  To: bigquery.Table{Dataset: "bronze", Name: "pedidos"}
  ```

- A regra, escrita: **um driver com SDK de fornecedor atrás mora no próprio
  pacote.** `from` e `to` guardam os que só precisam da biblioteca padrão; o
  BigQuery é `to/bigquery`, os object stores são `store/s3` e `store/gcs`, e
  `to/postgres` seguirá o mesmo caminho.

```
o que se importa                    pacotes   Google
sdk + from + to  (arquivos)             195   não
sdk + to/bigquery                       456   sim
```

O teste de poda passa a cobrir o **pipeline completo, dos dois lados** — que é
o caso que faltava.

---

## [0.20.0] — 2026-09-04

Fase 1 de [`docs/plan/2026-09-04-sdk-drivers-mvp.md`](docs/plan/2026-09-04-sdk-drivers-mvp.md):
arquivos, nos dois lados. Primeiro driver depois da costura.

### Adicionado
- **`from.Files` e `to.Files`** — leem e escrevem NDJSON, CSV, JSON e XML em
  disco, S3 ou GCS. O esquema do caminho diz o backend: `./entrada/*.csv`,
  `s3://bucket/dia=1/*.ndjson.gz`, `gs://bucket/landing/`.
- **`store/s3` e `store/gcs`** — os backends de object storage, cada um no seu
  pacote. Também servem MinIO, R2 e Ceph, por `BaseEndpoint`.
- `docker-compose.drivers.yml`, com MinIO, Postgres e MySQL para os testes de
  integração dos drivers.
- `examples/11-arquivos`, que roda de primeira e sem nuvem nenhuma.

### O backend é um valor, e esse é o ponto
Um driver de arquivos que importasse os três backends faria quem lê **CSV
local** compilar a AWS e o Google — contradizendo a regra que a fase 0 comprou.
Então `core.Store` é passado de fora:

```go
from.Files{Path: "./entrada/*.csv"}                          // nada extra
from.Files{Path: "s3://b/x/*.ndjson", Store: s3.New(client)} // só a AWS
```

```
o que se importa                 pacotes   AWS   Google
sdk                                  190   não   não
sdk + from     (inclui Files)        194   não   não
sdk + from + store/s3                265   sim   não
sdk + from + store/gcs               392   não   sim
```

Ler um CSV local custa **194 pacotes e zero SDK de nuvem**. Os testes de poda
em `examples/consumer/pruning_test.go` afirmam isso, com os controles.

### Alterado
- **O preview sobe para `internal/core`.** `ReadOptions.Preview` promete a
  tabela a todo driver, e só o HTTP honrava — seria campo morto no `from.Files`.
- **O metadado e o `CheckColumns` sobem para `internal/core`.** Dois writers não
  podem ter cópias do que calcula o `ingestion_id`: uma linha escrita em
  arquivo e a mesma linha no BigQuery têm de carregar o mesmo id. Era trabalho
  previsto para a fase 2 e chegou aqui porque o segundo destino o exigiu.

### Comportamento que vale a pena saber
- **Ordem de leitura é contrato.** Os arquivos são lidos ordenados, sempre; sem
  isso um `Key` posicional mudaria o `ingestion_id` entre execuções. Provado
  com um teste que roda cinco vezes, e outro contra o MinIO.
- **Escrita é atômica.** Temporário e rename em disco, um PUT só no objeto.
  Ninguém lê meio arquivo.
- **Um lote é um objeto.** Uma segunda carga não sobrescreve a primeira: um
  diretório não tem noção de "as mesmas linhas de novo".
- `to.Files` **recusa** `Dedup` — um diretório não tem chave para casar — e
  recusa Parquet, que traria o Arrow para quem só queria um arquivo.
- `.gz` pela extensão, e um `.gz` que não é gzip falha nomeando o arquivo em vez
  de virar "JSON inválido" três camadas adiante.

### Verificação
Integração de verdade contra MinIO (round-trip, ordem, gzip e **paginação com
1005 objetos**, porque uma listagem truncada que reporta sucesso parece só um
dia pequeno) e contra o bucket GCS real. Doze de BigQuery seguem passando.

---

## [0.19.0] — 2026-09-04

**BREAKING.** A costura para os drivers: fase 0 de
[`docs/plan/2026-09-04-sdk-drivers-mvp.md`](docs/plan/2026-09-04-sdk-drivers-mvp.md).
Nenhum driver novo — HTTP e BigQuery passam para trás das interfaces, e é isso
que torna Postgres, MySQL, Redshift e Files possíveis sem transformar `Source`
e `Target` em structs de união com quarenta campos.

### Adicionado
- **`sdk/from` e `sdk/to`** — um tipo por origem e por destino, cada um
  carregando a própria configuração e sabendo se ler ou se escrever:
  `from.HTTP`, `to.BigQuery`.
- **`sdk.Reader` e `sdk.Writer`**, com `ReadOptions` e `WriteOptions` para o
  que atravessa todos os drivers.

### Alterado
- **`Source` e `Target` passam a segurar o driver.** `Source{From: Reader}` mais
  preview e contadores; `Target{To: Writer}` mais `Columns`, `Metadata` e
  `Dedup`. Tudo que era específico de HTTP ou de BigQuery mudou de casa.
- **`Records` volta para o driver**, em `from.HTTP`. A `v0.18.0` o tinha movido
  para `Pipeline` porque `Source` era config e ele não; com o driver sendo um
  valor, `from.HTTP` **é** a origem HTTP inteira, e um `Pipeline.Records` seria
  um campo sem sentido para `from.Postgres` — exatamente o defeito que a fase 0
  existe para evitar em escala.
- `sdk.Extract` volta a receber dois argumentos; a leitura vai no driver.
- **`-dataset` e `-table` saem do `Execute`.** Eram flags de BigQuery num lugar
  genérico. Um fetcher que precisa delas registra as suas com `Flags` e monta o
  destino no `Before`, que é para o que os dois existem.
- `RunContext` e a resolução de configuração descem para `internal/core`, de
  onde os drivers alcançam.

### O número
```
                    pacotes   BigQuery
sdk                     190   não
sdk + from              194   não
sdk + to                456   sim
```
Antes: **458 pacotes e 21 MB de binário** para quem só importava o SDK, porque
a raiz puxava `sdk/load` e ele puxa BigQuery, Arrow e Thrift. Go poda por
pacote importado, nunca por campo usado — então a única forma de não pagar por
um driver é não importar o pacote dele. Há teste de consumidor que afirma isso,
**com o controle junto**: quem importa `to` tem de receber o BigQuery, ou o
teste passaria com um SDK que não carrega nada.

### Migração
```go
// antes
Source: sdk.Source{URL: "...", Timeout: 15 * time.Second},
Records: func(r sdk.Response) ([]any, error) { ... },
Target: sdk.Target{Dataset: "bronze", Table: "pedidos", CreateTable: sdk.Bool(true)},

// depois
Source: sdk.Source{
    From: from.HTTP{
        URL:     "...",
        Timeout: 15 * time.Second,
        Records: func(r sdk.Response) ([]any, error) { ... },
    },
},
Target: sdk.Target{
    To: to.BigQuery{Dataset: "bronze", Table: "pedidos", CreateTable: sdk.Bool(true)},
},
```

Um driver não implementado deixa de ser erro em tempo de execução e passa a ser
**erro de compilação**: não existe mais campo onde escrever um nome errado.

---

## [0.18.0] — 2026-09-04

**BREAKING.** Uma declaração de colunas, no formato do DDL. Executa
[`docs/plan/2026-09-04-sdk-uma-declaracao-de-colunas.md`](docs/plan/2026-09-04-sdk-uma-declaracao-de-colunas.md).

### Adicionado
- **`Target.Columns`** — as colunas do destino, na ordem do DDL, **incluindo as
  duas que o SDK preenche**. É a declaração que faltava: `ingestion_id` e
  `ingestion_loaded_at` não apareciam escritas em lugar nenhum do fetcher, e
  dentro da cadeia de `Transform` elas jamais poderiam — o SDK só as acrescenta
  depois, no load.

  Conferida de três jeitos: coluna declarada que nem o `Transform` nem o
  `Metadata` entregaram é erro nomeando a coluna; campo que a linha traz e a
  lista não declara é erro nomeando o campo; coluna declarada que a tabela real
  não tem é erro nomeando a coluna e as que a tabela tem.

  `nil` não declara e não confere nada. **Não há reserva**: essa lista é o único
  lugar onde as colunas do destino são declaradas.

### Alterado
- **`sdk.Schema` vira `sdk.Accept`.** Um fetcher real acabava com duas linhas
  `sdk.Schema` querendo dizer coisas diferentes — uma sobre o que se aceita da
  fonte, outra tentando ser a tabela. As duas verificações são legítimas e pegam
  coisas diferentes, então continuam duas; o que era errado era o nome.

  Não reusei o nome `Only`, que a spec sugere e está livre: ele existiu até a
  `v0.15.0` **descartando campo ausente em silêncio**, e devolver o mesmo nome
  com a semântica invertida é a troca silenciosa que a `v0.9.0` custou caro.
  `Accept` é nome novo, e diz o que a etapa faz.

- **`Records` sai de `Source` e vira campo de `Pipeline`.** `Source` passa a ser
  configuração e só isso — URL, headers, timeouts, retry, paginação, formato.
  `Records` era a única coisa lá dentro que decidia o que o dado significa, e
  agora fica ao lado do `Transform`, que é a outra etapa que roda sobre o
  extraído.

- `sdk.Extract` recebe a leitura como segundo argumento opcional:
  `Extract(ctx, source)` ou `Extract(ctx, source, leitura)`. Mais de uma é erro.
- `extract.JSON`, `extract.NDJSON`, `extract.CSV` e `extract.XML` recebem um
  `core.Reading` a mais. Passe `nil` para o comportamento padrão.

### Migração

```go
// antes
Source: sdk.Source{
    URL:     "...",
    Records: func(r sdk.Response) ([]any, error) { ... },
},
Transform: []sdk.Transformer{
    sdk.Schema("time", "temperature_2m", "latitude", "longitude"),
    sdk.Schema("provider", "entity", "payload", "source_key"),
},
Target: sdk.Target{
    Dataset: "bronze",
    Table:   "vendors_open_meteo_hourly_temperatures",
    Metadata: &sdk.Metadata{Provider: provider, Entity: entity, Key: ..., When: ...},
},

// depois
Source: sdk.Source{URL: "..."},

Records: func(r sdk.Response) ([]any, error) { ... },

Transform: []sdk.Transformer{
    sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
    // os Compute que montam provider, entity, source_key e payload
},

Target: sdk.Target{
    Dataset: "bronze",
    Table:   "vendors_open_meteo_hourly_temperatures",

    Columns: []string{
        "ingestion_id",        // do Metadata
        "ingestion_loaded_at", // do Metadata
        "provider",
        "entity",
        "source_key",
        "payload",
    },

    Metadata: &sdk.Metadata{Provider: provider, Entity: entity, Key: ..., When: ...},
},
```

Um teste de integração carrega essa tabela de seis colunas contra o BigQuery
real e confere que a declaração e o schema batem — e que uma declaração com
coluna a mais é recusada nomeando-a.

---

## [0.17.1] — 2026-09-03

### Corrigido
- **`Response.Object()` e `Response.JSON()` devolviam erro comum, não recusa.**
  Achado ao rodar a prova do §8 da spec com um consumidor de fora: uma página
  HTML de erro servida com 200 saía com `errors.Is(err, sdk.ErrRejected) ==
  false`. O `RejectIf` classificava certo, mas o exemplo da própria spec não
  passa por ele — chama `r.Object()` direto. Um corpo que não é o esperado é a
  fonte mandando algo que não é dado, com ou sem helper no meio.

---

## [0.17.0] — 2026-09-03

**BREAKING.** A validação é do consumidor, e roda por **resposta**. Executa
[`docs/plan/2026-09-03-sdk-validacao-do-consumidor.md`](docs/plan/2026-09-03-sdk-validacao-do-consumidor.md).

### Alterado
- **`Source.Guard` e `Source.Expand` viram `Source.Records`.** Eram a mesma
  pergunta — "o que esta resposta significa?" — partida em duas. Agora é uma
  função só, por resposta, que valida e fatia no mesmo lugar.

  ```go
  // antes
  Guard:  sdk.RejectIf("error"),
  Expand: sdk.ParallelArrays("hourly", "time", "temperature_2m"),

  // depois
  Records: func(r sdk.Response) ([]any, error) {
      if r.Status == http.StatusNoContent {
          return nil, nil // janela vazia é resultado, não falha
      }
      doc, err := r.Object()
      if err != nil {
          return nil, err
      }
      if bad, _ := doc["error"].(bool); bad {
          return nil, sdk.Reject("open-meteo recusou: %v", doc["reason"])
      }
      return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
  },
  ```

  `Records` **nil** mantém o padrão: decodifica e cada documento é um
  registro, pelo caminho que continua **streaming** — o que importa num NDJSON
  ou CSV grande. Defini-lo bufferiza a resposta, porque uma função que decide
  o que a resposta significa precisa vê-la inteira.

- **Todo 2xx chega ao `Records`.** Antes só o `200` passava: `201`, `204` e
  `206` derrubavam a execução com `http NNN` — reproduzido antes de consertar.
  Um vendor que responde `204` numa janela vazia não pode ser pipeline
  vermelho. Não-2xx continua como estava: erro com status e corpo, e retry
  onde já havia.

- `RejectIf` e `RequireFields` passam a receber `Response` em vez de
  `(status, body)`, para serem chamados de dentro do `Records`.

### Adicionado
- **`sdk.Response`** — `Status`, `Header`, `URL`, `Bytes()`, `Object()` e
  `JSON(&v)`. `Bytes()` não decodifica: procurar um marcador não paga o parse
  de um corpo que já se sabe ser lixo.
- **`sdk.Reject(formato, args...)`** e `sdk.ErrRejected`. Um `fmt.Errorf`
  também falha a execução, mas não se distingue de um mapa nil ou de um erro
  de digitação no fetcher — e esses dois pedem coisas diferentes de quem está
  de plantão. Recusa significa que o vendor mandou algo que não é dado:
  reexecutar a mesma janela vai dar no mesmo.
- `Records` junto de `DataKey` é **recusado**: os dois dizem onde estão os
  registros, e o `DataKey` ficaria sem efeito.

### Corrigido
- **`RejectIf` aceitava em silêncio corpo que não é JSON.** Uma página HTML de
  erro servida com 200 — portal em manutenção, WAF, proxy — passava pela
  guarda e falhava depois como "JSON inválido", apontando para o lugar errado.
  Era o único caso que a guarda existe para pegar e o único que ela deixava
  passar.

### Nota sobre a spec
O critério 8 ("`SkipRecord` aparece em pelo menos um exemplo executável") já
estava cumprido antes desta versão: `examples/09-transform/main.go:70`.

---

## [0.16.0] — 2026-09-03

### Adicionado
- **`Metadata.AutoID`** — `ingestion_id` vira um UUID aleatório por linha, e
  isso é a declaração inteira: nada do registro entra no id, então nada do
  registro precisa ser descrito. `Metadata: &sdk.Metadata{AutoID: true}` e
  pronto.

  O que ele abre mão é idempotência: a mesma leitura carregada duas vezes
  ganha ids diferentes. Por isso `DedupMerge` é **recusado** junto — um merge
  sobre id aleatório não casa com nada e escreveria exatamente as duplicatas
  que ele existe para evitar. E `Provider`/`Entity`/`Key`/`When` junto de
  `AutoID` também são recusados, nomeando os campos: seriam escritos e nunca
  lidos, que é o defeito que este SDK vive achando em si mesmo.

### Alterado
- **As duas colunas de metadado passam a ser declaradas**, em vez de
  inferidas:

  ```sql
  ingestion_id        STRING    NOT NULL,
  ingestion_loaded_at TIMESTAMP NOT NULL
  ```

  Autodetect infere as duas como `NULLABLE`, e o BigQuery **recusa** apertar
  uma coluna depois — verificado contra o BigQuery real antes de escolher a
  implementação (`Field ingestion_loaded_at has changed mode from REQUIRED to
  NULLABLE`). Então, com `Metadata`, o SDK cria a tabela ele mesmo.

  As colunas do cliente continuam tipadas **pelo BigQuery**: o schema sai de
  uma carga com autodetect numa tabela descartável, e o SDK sobrepõe só as
  duas que são dele. Adivinhar que um `float64` do `encoding/json` significa
  `FLOAT64` colocaria a inferência de volta pela porta dos fundos, justo nas
  colunas menos indicadas para isso. Custa um job a mais, na execução que cria
  a tabela e nunca mais.

- Partição e clusterização passam a ser definidas na criação da tabela, não no
  job de carga, porque é lá que a tabela nasce agora.
- Documentação: `Metadata` é um **interruptor para essas duas colunas, não um
  lugar para pôr dado**. Nada escrito no bloco vira coluna.

---

## [0.15.0] — 2026-09-03

**BREAKING.** As colunas são compostas no `Transform`, e o SDK não inventa
nenhuma.

### Adicionado
- **`sdk.Schema(campos...)`** — um `Transformer` que declara as colunas do
  destino: exatamente esses campos, e erro nomeando qualquer um que falte.
  É a camada de proteção: um campo que a fonte para de mandar vira erro, não
  uma coluna que silenciosamente foi para NULL. Colocado por último na cadeia,
  ele responde numa linha "que colunas essa tabela tem?".
- **`sdk.Metadata`** — bloco que reúne o que só existe para o `ingestion_id`:
  `Provider`, `Entity`, `Key` e `When`. Declará-lo é o que pede os dois campos
  de metadado, e nomeia no ponto de chamada as duas colunas que o SDK
  acrescenta — nenhuma coluna aparece na tabela sem estar escrita no fetcher.

### Alterado
- **`Target.ExtraMetadata bool` → `Target.Metadata *Metadata`.** `nil` não
  acrescenta nada. A regra "proveniência só é exigida com metadado" deixa de
  ser validação e passa a ser a forma da API: sem o bloco não existe `Key` nem
  `When` para o SDK chamar. É a garantia no lugar mais forte.
- `Target.Provider`, `Target.Entity`, `Target.Key` e `Target.When` **saem** do
  `Target` e passam a viver dentro do `Metadata`. O `Target` volta a ser só
  destino.
- `LoadConfig.ExtraMetadata` → `LoadConfig.Metadata`; `WithExtraMetadata` →
  `WithMetadata`.
- **`sdk.Only` sai, substituído por `sdk.Schema`.** Mesma assinatura, e a
  diferença é a que importa: o `Only` descartava em silêncio um campo ausente,
  que é exatamente o modo de falhar que o resto do SDK combate.

### Corrigido
- `03-basic-load` rodava e falhava com "table does not exist". Ganhou
  `WithCreateTable(true)` e foi **executado de verdade** contra o BigQuery: a
  tabela sai com as 2 colunas do chamador mais exatamente as 2 do metadado, e
  nenhuma de `provider`, `entity`, `source_key` ou `payload`.
- O erro de um registro que não é objeto com o bloco ligado começava com
  maiúscula, contra o ST1005.

### Migração

```go
// antes
Target: sdk.Target{
    Provider: "open_meteo", Entity: "hourly",
    Key: sdk.Key("latitude", "time"), When: sdk.Field("time"),
    ExtraMetadata: true,
}

// depois
Transform: []sdk.Transformer{
    sdk.Schema("time", "temperature_2m", "latitude", "longitude"),
},
Target: sdk.Target{
    Table: "vendors_open_meteo_hourlys",
    Metadata: &sdk.Metadata{
        Provider: "open_meteo", Entity: "hourly",
        Key: sdk.Key("latitude", "time"), When: sdk.Field("time"),
    },
}
```

Trocar `sdk.Only` por `sdk.Schema` é textual, mas o comportamento muda: um
campo nomeado e ausente passa a ser erro.

---

## [0.14.0] — 2026-09-03

**BREAKING**, e é uma retirada de responsabilidade: o payload é do cliente.

### Alterado
- **`Provider`, `Entity` e `Key` passam a ser exigidos só com
  `ExtraMetadata`.** Eles existem para construir o `ingestion_id` e nada mais,
  então são necessários exatamente quando o SDK vai carimbar um. Antes eram
  obrigatórios sempre, mesmo numa carga que não adicionava metadado nenhum.
- **Sem `ExtraMetadata`, o SDK não lê um campo sequer do payload.** `Key` e
  `When` não são chamados: não faz sentido o SDK aprender a ler o registro
  para escrever uma coluna que ele não vai escrever — e um seletor que erra
  derrubava uma carga que nunca pediu inspeção nenhuma. A proveniência
  (`Provider`, `Entity`, `SourceKey`, `RecordTS`) também deixa de ser
  carimbada no envelope quando ninguém a consome.
- `Target.Table` passa a ser exigido quando `Provider` e `Entity` estão
  vazios. Sem os dois não há nome padrão para cair, e `vendors__s` são dois
  valores ausentes se passando por um.
- `-dry-run` deixa de imprimir `ingestion_id`, `key` e `ts` quando
  `ExtraMetadata` está desligado. Imprimir um id calculado ali mostraria uma
  coluna que nunca vai pousar.

### Corrigido
- O erro de um payload que não é objeto com `ExtraMetadata` ligado dizia
  `unmarshal to map: json: cannot unmarshal string into Go value of type
  map[string]interface {}`. Agora diz o que fazer.

### Migração

Uma carga que já usava `ExtraMetadata: true` não muda. Uma que não usava pode
apagar `Provider`, `Entity`, `Key` e `When`, e precisa definir `Table` se
dependia do nome padrão:

```go
// antes
sdk.Target{Provider: "open_meteo", Entity: "hourly", Key: sdk.Key("id")}
// depois
sdk.Target{Table: "vendors_open_meteo_hourlys"}
```

Dois testes de integração novos verificam no BigQuery de verdade que a tabela
criada tem exatamente as colunas do chamador, e que com a flag ligada tem
exatamente essas mais duas.

---

## [0.13.0] — 2026-09-03

### Adicionado
- **`Source.Preview`** — imprime os primeiros N registros como tabela quando o
  extract termina, no espírito do `head()` de um dataframe. Desligado por
  padrão. Responde "o que eu puxei, afinal?" sem depurador e sem drenar o fluxo
  para uma variável só para olhar. A amostra é colhida enquanto os registros
  passam, então custa N registros de memória e não altera nada do que o
  consumidor recebe — e sai também quando a fonte morre no meio ou o consumidor
  sai do laço, que é justamente quando se quer ver o que chegou.
- `PreviewBytes` (padrão 4096) corta o bloco: linhas caem de baixo para cima e
  o rodapé diz quantas ficaram de fora. Colunas largas demais para a linha são
  elididas com a contagem. Um preview que mostrasse menos do que amostrou sem
  dizer estaria mentindo sobre a amostra.
- `PreviewWriter` (padrão `os.Stderr`). A tabela não passa por `slog` porque o
  `TextHandler` escapa quebras de linha, e o bloco chegaria como uma única
  linha ilegível de `\n`. Os contadores passam, que é onde número estruturado
  pertence.
- Flag `-preview N` no `Execute`, para ligar sem recompilar. Ela só liga: uma
  pipeline que pediu preview em código continua com o dela.
- **`Stats.Bytes`**, `Data.Stats().Bytes` e `Result.ExtractBytes` — o tamanho do
  que veio pelo fio, antes do `Transform`. É o número que explica um extract
  lento; uma página pode ser quase toda envelope e ainda demorar um minuto.
- O log `extract complete` ganhou `bytes` e `per_page`.

### Corrigido
- Durações abaixo de um milissegundo eram arredondadas para `0s`, que se lê
  como "não medido" em vez de "rápido". Agora o arredondamento acompanha a
  escala.

### Removido
- `core.ExtractOption`, declarado e usado por nada: nenhuma opção, nenhum
  consumidor, nem reexportado. Era inalcançável de fora do módulo.

---

## [0.12.1] — 2026-09-03

### Corrigido
- **O exemplo de `LoadConfig` no README não compilava.** Trazia
  `DeleteAfterLoad: true`, campo renomeado para `KeepStagedFile` na 0.11.0 —
  quem copiasse o bloco recebia `unknown field DeleteAfterLoad`. É o README que
  o pkg.go.dev renderiza, então ele sai numa tag, não num commit solto.

### Adicionado
- README documenta a reconciliação de colunas do `DedupMerge`, que a 0.12.0
  introduziu: a carga passa a ser **recusada** quando as linhas trazem coluna
  que o destino não tem.

---

## [0.12.0] — 2026-09-03

Conserto do MERGE, a partir do relatório em
[`docs/plan/2026-09-03-sdk-conserto-do-merge.md`](docs/plan/2026-09-03-sdk-conserto-do-merge.md),
escrito por quem consome o SDK.

### Corrigido
- **O MERGE casava colunas por posição, não por nome.** A 0.9.0 trocou a lista
  de colunas por `INSERT ROW` afirmando, no código e na mensagem de commit, que
  o BigQuery casa por nome. Ele casa por posição. Como o schema da tabela
  temporária vem de autodetect sobre o payload, a ordem não está sob controle
  de ninguém: num destino de schema fixo o `latitude` do consumidor caiu em
  `ingestion_id` e a carga morreu com `Value has type FLOAT64 which cannot be
  inserted into column ingestion_id`. Os testes existentes não pegavam porque
  deixavam o próprio SDK criar o destino a partir do mesmo lote, e as duas
  ordens coincidiam por acidente. O `INSERT` agora nomeia as colunas, na ordem
  do destino, com crase em todo identificador — `full`, `range` e `comment` são
  reservadas e aparecem em payload de verdade.

### Adicionado
- `reconcile` compara os dois schemas antes de montar o SQL, e é assimétrica de
  propósito: coluna que as linhas trazem e o destino não tem é **erro** nomeando
  a coluna, porque descartar dado em silêncio é o pior modo de falhar; coluna do
  destino que as linhas não trazem fica NULL, que é legítimo; tipo incompatível
  no mesmo nome é erro nomeando a coluna e os dois tipos.
- `mergeSQL` e `reconcile` são funções puras, testadas sob `-short`. O SQL era
  montado dentro de um método que precisa de cliente BigQuery, e é por isso que
  nenhum teste unitário jamais tinha visto a string gerada.
- Teste de integração que carrega num destino cuja ordem de colunas difere da
  que o autodetect produz. Ele falha na 0.11.0 com o erro exato que o consumidor
  reportou, e verifica os valores de volta — o modo posicional também sabe
  passar sem erro e gravar tudo na coluna errada.

---

## [0.11.0] — 2026-09-03

Quatro defeitos que só o BigQuery real revelou: os testes de integração
existiam desde a 0.2.1 e rodaram pela primeira vez.

### Corrigido
- **`CreateTable` e `DedupMerge` não compunham.** Com merge a carga vai para uma
  temporária, e era o job de carga que criava o destino — que portanto nunca
  passava a existir, e o MERGE falhava com `table not found`. Numa primeira
  carga não há contra o que deduplicar, então o merge cede o lugar ao caminho
  comum e o resultado reporta honestamente que a dedup não rodou.
- **`Load` mutava a fatia do chamador.** Variádico compartilha o array de fundo,
  então carimbar o metadado escrevia no lote que o chamador ainda segura, e a
  segunda carga do mesmo lote falhava com `payload already has ingestion_id` —
  exatamente o que um retry faz.
- **`ClusterBy` só falhava depois do job submetido.** Com autodetect o schema sai
  das linhas, então a validação agora acontece antes e diz qual campo falta.

### Alterado
- **`DeleteAfterLoad` virou `KeepStagedFile`.** Documentado como *default: true*,
  que um `bool` não consegue ser: quem usa `load.New` direto recebia o zero
  value, `false`, e nada era limpo. O zero value do novo nome apaga.

---

## [0.10.1] — 2026-09-03

### Adicionado
- O dispatcher liga no Runner: a engine passa `BRAVIS_RUN_*` ao processo do
  passo, com o histórico decidindo se é a primeira execução bem-sucedida.

### Corrigido
- `RunContext.Attempt` era documentado contando de 1; a engine conta de 0
  (`task_runs.attempt DEFAULT 0`).
- `BRAVIS_RUN_ID` era injetado como UUID zero fora de um run de verdade. O SDK
  detecta "sob a Bravis" pela presença do id, então um fetcher rodado à mão
  logava um id falso. As variáveis de identidade só saem com `RunID` real.

---

## [0.10.0] — 2026-09-03

### Adicionado
- `RunContext` — a engine passa contexto de execução ao SDK sem o consumidor
  plumbá-lo: `ID`, `First`, `Attempt`, `Trigger`, `LogicalDate` e `Params`, por
  ambiente. Quem usa só o SDK não precisa de nada disso.
- `Target.CreateTable` virou `*bool`, porque dois estados não bastam: `nil` é
  "não falei" e deixa a engine decidir (primeira execução do passo, ou
  `create_table=true` no dispatch); uma recusa explícita vence a engine. Um
  `bool` faria o zero value significar as duas coisas.

---

## [0.9.1] — 2026-09-03

### Corrigido
- **Três opções existiam e nenhum consumidor podia chamá-las.**
  `WithCreateSQL`, `WithPartitionExpiration` e `WithRequirePartitionFilter`
  eram declaradas em `internal/core` e nunca reexportadas na raiz. Há teste que
  lê os dois arquivos e falha se alguma `With*` do core não tiver reexport.

---

## [0.9.0] — 2026-09-03

**BREAKING.** O SDK deixa de impor colunas.

### Removido
- **O contrato de seis colunas.** `WriteEnvelopeColumns`, o schema de landing, a
  verificação das seis colunas e o `AddMetadata` antigo que dobrava `provider`,
  `entity`, `source_key` e `record_ts` para dentro do payload. O load escreve o
  payload como o `Transform` o deixou, e nada mais: a estrutura da linha é
  decisão de quem chama. Quem quiser as seis colunas monta num `Transformer` —
  é o exemplo `07-own-shape`.
- `MetadataNamespace` e `WithMetadataNamespace`, que eram aceitos, validados,
  default-ados e ignorados: `IngestionID` fixa o namespace.
- `SourceKeyField`, declarado e nunca lido.

### Adicionado
- `ExtraMetadata`, default `false`, que adiciona exatamente dois campos:
  `ingestion_id` e `ingestion_loaded_at`. `Provider`, `Entity` e `SourceKey`
  seguem sendo proveniência — constroem o id, não viram coluna.
- `CreateTable` (default `false`, nada roda DDL sem ser pedido), com o schema
  inferido dos dados. `CreateSQL` roda a DDL do cliente e depois confere que ela
  produziu a tabela certa.
- `PartitionExpiration` (zero mantém: apagar dado não é algo que biblioteca
  começa a fazer sozinha), `RequirePartitionFilter`, `ClusterBy`, e descrição
  mais labels na tabela criada.
- `RequirePartitionFilter` é recusado junto de `DedupMerge`: o merge casa por
  `ingestion_id` em todas as partições e não dá para escopar, já que
  `ingestion_loaded_at` é o momento da carga.

> A troca da lista de colunas do MERGE por `INSERT ROW`, feita aqui, estava
> errada e foi desfeita na 0.12.0.

---

## [0.8.0] — 2026-09-03

### Adicionado
- `Data.Stats()` — os contadores de extract já existiam, mas só o `Result` final
  os expunha, e os testes internos liam o campo privado. Um consumidor não
  conseguia.

---

## [0.7.0] — 2026-09-03

**BREAKING.** Três superfícies que não faziam o que diziam.

### Corrigido
- `applyLayout` era escrita e nunca chamada por nenhum dos dois caminhos de
  carga, o que fazia de `CreateTable` uma flag sem efeito.
- `Result.Pages` e `Result.Attempts` eram sempre zero.
- `examples/` nunca compilou — cinco `func main()` num diretório, sem módulo, e
  um `extract.Fonte` que nunca existiu. A CI escondia com `|| true`.

---

## [0.6.0] — 2026-09-02

### Adicionado
- **Passo `Transform` entre `Extract` e `Load`.** O consumidor passa funções que
  recebem o payload e devolvem o payload transformado, preguiçosamente. Sai
  `SkipRecord` para descartar um registro, e os auxiliares `Only`, `Without`,
  `Rename` e `Compute`.

---

## [0.5.0] — 2026-09-02

**BREAKING.** Os campos de metadado perderam o prefixo `_bravis_`.

---

## [0.4.1] — 2026-09-02

### Corrigido
- Descrições e um nome de flag que escaparam da tradução para inglês.

---

## [0.4.0] — 2026-09-02

**BREAKING.** A API inteira em inglês.

### Alterado
- `Fonte` → `Source`, `Rodar` → `Run`, e o resto dos identificadores públicos.
- `Driver` nos dois lados, extract e load, nomeando o backend (HTTP,
  BigQuery) em vez de deixá-lo implícito.

---

## [0.3.0] — 2026-09-02

### Adicionado
- **A API de duas chamadas**: `Extract` devolve `*Data`, `Load` o consome.
- Configuração por ambiente, com precedência documentada: o que você definiu,
  depois a engine, depois o ambiente, depois o default, depois erro.
- `LICENSE` e a higiene que um projeto aberto precisa ter.

---

## [0.2.1] — 2026-09-02

Conserto do `load`, conforme [`docs/SDK_LOAD.md`](docs/SDK_LOAD.md).

### Adicionado
- `WriteEnvelopeColumns` / `WithEnvelopeColumns` — modo opt-in que escreve o
  contrato de 6 colunas (`ingestion_id`, `ingestion_loaded_at`, `provider`,
  `entity`, `source_key`, `payload`) com o payload aninhado. Existe para o
  `ingestion_id` ter um dono único: usa `Envelope.IngestionID()`, a mesma
  função, e há teste que falha se as duas divergirem.
- Teste de integração contra BigQuery real, travado em `-short` e em
  `BRAVIS_IT_PROJECT`. É o único que prova que uma linha realmente entra.

### Corrigido
- **A estratégia inline era streaming insert, não lote.** `table.Inserter()` é
  cobrado por linha e as linhas ficam num buffer invisível ao DML por até 90
  minutos. As duas estratégias agora são load jobs, diferindo só na fonte.
- **`LoadResult.ErrorRows` nunca era preenchido**, e `Load` devolvia `nil` em
  todo caminho de erro — enquanto o README documentava lê-lo depois de uma
  falha. Esse trecho documentado causava panic. `Load` sempre devolve um
  resultado, e os erros por linha vêm de `job.Status.Errors`.
- `loadViaGCS` deriva `SourceFormat` de `cfg.Format` em vez de fixar.

### Alterado
- **`Format` recusa `"csv"` e `"parquet"`** em vez de aceitá-los e escrever
  NDJSON assim mesmo. `WithFormat("parquet")` reportava uma carga Parquet que
  nunca acontecia — número errado na telemetria é pior que número ausente.
  `LoadResult.Format` reporta o formato efetivamente escrito.
- `AddMetadata` e `WriteEnvelopeColumns` são mutuamente exclusivos; `New`
  recusa os dois juntos.

---

## [0.2.0] — 2026-09-02

Implementa a superfície que a documentação já anunciava e nenhum código lia.

### Adicionado
- **Paginação** — header `Link` (RFC 8288), cursor no corpo e offset. Novos
  campos `FollowLinks`, `DataKey` e `MaxPages`. `CursorKey`, `OffsetKey` e
  `PageSize` existiam na struct e nunca eram lidos.
- **Rate limiting** — `Fonte.RateLimiter` era `any`, então nada podia ser
  chamado nele. Virou a interface `Limiter` (`Wait(ctx) error`), que
  `*rate.Limiter` satisfaz sem o SDK herdar a dependência.
- **Decoder XML** — `extract.XML()` sempre falhava com `unsupported format:
  xml`, porque `NewDecoder` não tinha o case.
- `load.New` passou a ser variádico e aceitar as 8 opções `With*`, que antes
  não podiam ser passadas a lugar nenhum. Aceita config nula e nunca muta a do
  chamador.
- Controle de cabeçalho em CSV via `NoHeader`.

### Corrigido
- **Corpos truncados.** O contexto da tentativa era cancelado logo após
  `client.Do`, mas o corpo ainda transmitia sob ele — qualquer payload não
  pré-bufferizado morria no meio com `context canceled`.
- **Laço infinito.** Um erro de decoder era emitido e seguido de `continue`,
  mas erro de sintaxe JSON se repete para sempre. O iterador girava emitindo o
  mesmo erro (observado: mais de 5GB de saída).
- `loadViaGCS` não definia `SourceFormat`, então o BigQuery lia o NDJSON
  encenado como CSV — toda carga acima de 5000 linhas era corrompida.
- `loadInline` usava `bigquery.StructSaver` com um `json.RawMessage`.
  `StructSaver` reflete sobre campos de struct; um `[]byte` não tem nenhum.

### Alterado
- **Go mínimo baixado de 1.25.7 para 1.23**, que é o piso real (`iter.Seq2`).
  O 1.25.7 restringia quem podia consumir sem dar nada em troca.

---

## [0.1.1] — 2026-09-02

Primeira versão que compila.

### Corrigido
- Imports não usados que impediam a compilação.
- `gcsRef.Format` e `bigquery.NDJSON`, que não existem na API do BigQuery.
- Import de `github.com/zarvhq/bravis/sdk` num teste, caminho que não existe.
- Cinco dependências indiretas fixadas em revisões inexistentes.

---

## [0.1.0] — 2026-09-02 — **NÃO USE**

> Publicada quebrada: o `go.mod` fixava
> `github.com/golang/groupcache@v0.0.0-20210921142519-41873776e32e`, revisão que
> não existe. Não compila para ninguém.
>
> **O proxy de módulos do Go é imutável.** Apagar a tag no git não remove a
> versão de `proxy.golang.org`, então ela permanece publicada e quebrada para
> sempre. Comece pela `v0.1.1`.

[0.22.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.22.0
[0.21.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.21.1
[0.21.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.21.0
[0.20.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.20.0
[0.19.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.19.0
[0.18.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.18.0
[0.17.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.17.1
[0.17.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.17.0
[0.16.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.16.0
[0.15.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.15.0
[0.14.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.14.0
[0.13.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.13.0
[0.12.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.12.1
[0.12.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.12.0
[0.11.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.11.0
[0.10.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.10.1
[0.10.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.10.0
[0.9.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.9.1
[0.9.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.9.0
[0.8.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.8.0
[0.7.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.7.0
[0.6.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.6.0
[0.5.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.5.0
[0.4.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.4.1
[0.4.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.4.0
[0.3.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.3.0
[0.2.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.2.1
[0.2.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.2.0
[0.1.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.1.1
[0.1.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.1.0
