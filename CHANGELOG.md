# Changelog

Versões do SDK (`github.com/AreteAcademy/bravis/sdk`). O formato segue
[Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/), e as versões seguem
[SemVer](https://semver.org/lang/pt-BR/).

A tag de um módulo aninhado leva o prefixo do diretório: `sdk/v0.2.1`.

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
