# Changelog

Versões do SDK (`github.com/AreteAcademy/bravis/sdk`). O formato segue
[Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/), e as versões seguem
[SemVer](https://semver.org/lang/pt-BR/).

A tag de um módulo aninhado leva o prefixo do diretório: `sdk/v0.2.1`.

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

[0.2.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.2.1
[0.2.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.2.0
[0.1.1]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.1.1
[0.1.0]: https://github.com/AreteAcademy/bravis/releases/tag/sdk%2Fv0.1.0
