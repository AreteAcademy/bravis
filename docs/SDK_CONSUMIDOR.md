# O SDK visto pelo primeiro consumidor

**Vale para** `sdk/v0.23.0` · **Escrito em** 2026-09-04 · **Revisado em** 2026-09-04

Registro do que o consumidor `zarv-data-pipeline` achou entre 2026-09-02 e
2026-09-04, e do que mudou no SDK por causa disso. **31 versões em três dias.**

Este documento não repete [`SDK_DECISOES.md`](SDK_DECISOES.md) (o que cada
decisão custou) nem [`SDK_ARQUITETURA.md`](SDK_ARQUITETURA.md) (como o desenho
ficou). Ele guarda a outra metade: **como cada defeito apareceu**, o que ele
custou antes de aparecer, e quais classes de defeito se repetiram — que é o que
serve de checklist para a próxima versão.

---

## 1. O consumidor, em uma frase

Um fetcher em Go — `scripts/vendors/exemplo_go` — que busca a previsão horária do
Open-Meteo e escreve numa landing do BigQuery com **seis colunas declaradas em
DDL do dbt**:

```sql
ingestion_id STRING NOT NULL, ingestion_loaded_at TIMESTAMP NOT NULL,
provider STRING NOT NULL, entity STRING NOT NULL,
source_key STRING, payload JSON NOT NULL
```

Duas propriedades desse consumidor explicam quase tudo que ele achou:

- **A tabela é dele, não do SDK.** O DDL vive em
  `dbt/macros/config/setup_vendors.sql`, e o SDK escreve numa tabela que não
  criou. Isso exercitou o caminho "conferir contra tabela existente", que é o
  modo normal de um warehouse com dono e o menos testado do SDK.
- **`payload` é `JSON`.** É o tipo certo para payload de vendor, e foi a forma
  que descobriu o §6 — a última coisa que a deduplicação não servia.

Ele roda de hora em hora em dev desde 2026-09-03, e a cada troca de API do SDK a
prova exigida foi a mesma: **os `ingestion_id` têm de continuar batendo.** Nas
cinco trocas (`v0.8.0` → `v0.15.0` → `v0.17.1` → `v0.18.0` → `v0.22.0`), bateram.
Nenhuma linha foi reingerida.

---

## 2. O que foi achado, e como

Onze defeitos, em dois relatórios: [`SDK_LOAD.md`](SDK_LOAD.md) (`v0.1.1`) e
[`SDK_V9.md`](SDK_V9.md) (`v0.9.0` em diante).

| # | defeito | como apareceu | resolvido |
|---|---|---|---|
| L1 | `loadInline` não escrevia uma linha: `StructSaver` com `json.RawMessage` | chamando `Save()` direto | `v0.2.1` |
| L2 | `loadViaGCS` não definia `SourceFormat`; vazio é CSV, e o arquivo era NDJSON | leitura do código | `v0.2.1` |
| L3 | `Format` aceitava `csv`/`parquet` e gravava NDJSON — `LoadResult.Format` mentia | leitura do código | `v0.2.1` |
| L4 | o caminho "inline" era streaming insert, não lote | leitura do código | `v0.2.1` |
| L5 | `ErrorRows` declarado e nunca preenchido; `Load` devolvia `nil` no erro, e o README mandava lê-lo | o trecho documentado causava panic | `v0.2.1` |
| L6 | `CursorKey`, `OffsetKey`, `PageSize`, `RateLimiter`, `HasHeader` declarados e lidos em nenhum lugar | `grep`, uma ocorrência cada | `v0.2.0` |
| 1 | `CreateTable` + `DedupMerge` não criava a tabela: 404 | três execuções contra o dev | `v0.12.0` |
| 2 | `INSERT ROW` casa por **posição**, e o comentário afirmava "by name" | duas tabelas com os mesmos nomes em ordem trocada | `v0.12.0` |
| 4 | as seis colunas deixaram de ser alcançáveis na `v0.9.0` | erro de tipo do BigQuery, dias depois | `v0.15.0` |
| 5 | `Preview` amostrava antes do `Expand`: `1 row` no rodapé, `24 records` na linha seguinte | rodando com `-preview` | `v0.19.0` |
| 6 | a temporária do merge nascia por autodetect: `JSON` virava `RECORD` | subindo para a `v0.15.0` | `v0.23.0` |
| 7 | `FormatError.Format` declarado, interpolado e nunca preenchido | dois espaços na mensagem de erro | `v0.23.0` |
| 3 | `msg=loaded` em INFO numa carga que não carregou | leitura do log de uma falha | `v0.23.0` |

**Os onze estão fechados.** O §3 era o último: `pipeline.go` logava `"loaded"`
sempre que existia resultado, e existe resultado em todo caminho de erro — por
desenho, desde a `v0.2.1`, para que `ErrorRows` seja legível depois da falha. As
duas decisões estavam certas isoladas; juntas produziam `msg=loaded` numa carga
que não carregou, **em INFO**, onde quem observa `ERROR` nem via.

Reproduzido antes de consertar, com um destino que devolve resultado e erro
juntos:

```
level=INFO msg=loaded pipeline=destino.falho records=2 lines=0 ...
```

A mensagem passou a depender do erro: `load failed` em `ERROR`, com os
contadores e o erro junto. E `Execute` foi partido em dois, porque ele instala
o logger padrão e nenhum teste conseguia ler o que ele escrevia — a linha de
log é a observabilidade inteira de um fetcher, e era a parte sem teste.

---

## 3. As classes que se repetiram

Cinco defeitos diferentes, uma causa só. É a parte deste documento que serve como
checklist.

### 3.1 Campo público declarado e nunca preenchido

`ErrorRows` (L5), os cinco do L6, `DeleteAfterLoad` documentado como
`default: true` que um `bool` não conseguia ser, e o `FormatError.Format` (§7).
Quatro vezes em 23 versões.

> **Campo público que não faz nada é pior que campo ausente**, porque quem o
> preenche acredita ter configurado algo. O teste que pega isso é um `grep` por
> ocorrências: uma só é a declaração.

### 3.2 Telemetria que afirma sucesso num caminho de erro

`LoadResult.Format` reportando Parquet enquanto gravava NDJSON (L3), `ErrorRows`
vazio depois de uma falha (L5), `msg=loaded` antes do `msg=failed` (§3), e
`TableCreated` calculado a partir de um `existed` que não descrevia o que
aconteceu (§1).

> **Número errado é pior que número ausente, porque ninguém desconfia dele.**

### 3.3 Default que decide sem aparecer no código de quem chama

`WriteEnvelopeColumns: !RawPayload` era o **padrão** na `v0.8.0`: o fetcher
declarava `Provider` e `Entity` como procedência, e o SDK os promovia a colunas,
aninhava o payload e derivava `source_key` — nada disso escrito no `main.go`.

Foi esse ocultamento que permitiu à `v0.9.0` parar de preencher três das seis
colunas **sem que nada do lado do consumidor acusasse**. A tabela continuou
existindo, com as colunas lá, vazias. O sintoma chegou dias depois como um erro
de tipo do BigQuery.

> O teste é uma pergunta: *"onde estão declaradas as colunas desta tabela?"* Se a
> resposta não é um lugar no código de quem chama, há um default decidindo.

### 3.4 Atalho que ocupa o lugar da interface

`Guard: sdk.RejectIf("error")` e `Expand: sdk.ParallelArrays(...)` fizeram o
consumidor concluir que o SDK só sabia testar um campo de topo e fatiar arrays
paralelos — quando os dois campos **sempre foram funções livres**. E `Schema`
usado duas vezes com sentidos diferentes fez ninguém conseguir dizer qual das
duas linhas era a tabela.

> Um atalho que esconde a decisão acaba **tomando** a decisão.

### 3.5 O teste existia e nunca tinha rodado

`TestIntegrationMergeDoesNotDouble` cobria exatamente o §1 — tabela ausente,
`CreateTable`, `DedupMerge` — desde a `v0.2.1`. Nunca rodou: `requireIntegration`
pula sem `BRAVIS_IT_PROJECT`, e a variável não estava definida em nada
automatizado. Quando finalmente rodou, achou **quatro** defeitos a mais numa
única execução, incluindo `Load` mutando a fatia do chamador — que quebrava
exatamente o retry que o `DedupMerge` existe para tratar.

> O defeito não escapou por falta de teste. Escapou porque o teste estava atrás
> de uma variável de ambiente.

### 3.6 O teste cobria só a forma fácil

Todo teste de merge usava **coluna escalar**. O §6 — `JSON` virando `RECORD` —
sobreviveu 14 versões porque nenhum exercitava o tipo que motiva a landing de um
vendor. O teste que o pega carrega duas vezes e lê o valor de volta com
`JSON_VALUE`: uma string que por acaso contém JSON satisfaria uma contagem de
linhas e nada mais.

---

## 4. As três reviravoltas, e o que as encerrou

A pergunta "quem produz as colunas?" mudou de resposta três vezes: agnóstico na
`v0.1.1`, contrato opt-in na `v0.2.1`, agnóstico de novo na `v0.9.0` — esta
última sem uma linha de changelog, que é como o consumidor descobriu por erro de
tipo.

O que encerrou não foi escolher um lado. Foi **tirar a escolha do SDK**: o
`Transform` compõe a linha e `Target.Columns` declara o destino, então as duas
formas — envelope de seis colunas e tabela plana — passaram a ser a mesma
declaração com origens diferentes. Enquanto era o SDK que decidia, a resposta ia
mudar de novo.

Vale registrar que **duas propostas do consumidor foram recusadas com razão**:

- reusar o nome `Only` para a etapa de moldagem. Ele existiu até a `v0.15.0`
  descartando campo ausente em silêncio, e devolver o mesmo nome com semântica
  invertida é a troca silenciosa que a `v0.9.0` custou caro. Virou `Accept`.
- pôr a validação dentro do `Transform`, ao pé da letra do pedido. `Transformer`
  roda por registro, e uma resposta de erro produz zero registros — o validador
  nunca seria chamado sobre ela. Virou `Records`, por resposta.

---

## 5. O fetcher hoje

```go
origem := &from.HTTP{Header: …, Timeout: …, TotalTimeout: …,
    Records: func(r sdk.Response) ([]any, error) { … }}   // o que a resposta significa

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: origem},                  // de onde vem
    Transform: []sdk.Transformer{
        sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
        sdk.Compute("payload", …), sdk.Compute("provider", …),
        sdk.Compute("entity", …), sdk.Compute("source_key", sdk.Key(…)),
        sdk.IngestionID("provider", "entity", "source_key", "time"),
        sdk.IngestionLoadedAt(),
        sdk.Without("time", "temperature_2m", "latitude", "longitude"),
    },                                                    // que linha monta
    Target: sdk.Target{
        To:       bigquery.Table{Dataset: "bronze", Name: "vendors_open_meteo_hourly_temperatures"},
        Columns:  []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload"},
        Dedup:    sdk.DedupMerge,
    },                                                    // para onde vai, e com que colunas
})
```

Três detalhes que só um consumidor real produz, e que valem para o próximo:

- **`sdk.IngestionID` lê `source_key` de volta** em vez de recalculá-lo. O
  `Transform` o computa uma vez com `sdk.Key(...)`, e o transformer o lê da
  linha. Um só lugar produz a chave, então a coluna e o `ingestion_id` não podem
  divergir.
- **Um adaptador desce um nível e delega.** Os seletores rodam depois do
  `Transform`, quando a leitura já está aninhada sob `payload`. O adaptador
  chama o `sdk.Key` do SDK sobre o mapa interno — um `fmt.Sprintf` local
  pareceria idêntico e daria outro `ingestion_id` no primeiro float formatado
  diferente.
- **O id tem de ser estável.** A dedupe deste consumidor acontece no bronze, com
  `ROW_NUMBER() OVER (PARTITION BY ingestion_id)`, e depende de o mesmo
  `(lat, lon, hora)` dar sempre o mesmo id. É o que `sdk.IngestionID` garante, e
  o motivo de a fórmula ser congelada.

---

## 6. O que fica para a próxima versão

1. ~~**O §3**~~ — fechado na `v0.23.0`.

2. **A integração na CI.** Metade feita, e a metade que falta depende de você.

   O job `integration` existe agora e roda **os testes de S3 a cada push**,
   contra um MinIO que sobe ao lado — sem segredo nenhum. Era a lição 3.5, e
   essa parte não depende de mais ninguém.

   O BigQuery ainda não roda: precisa de credencial. O job já está escrito e
   liga sozinho quando existirem, e avisa em voz alta enquanto não existirem:

   | o que criar | onde | o quê |
   |---|---|---|
   | secret `GCP_CREDENTIALS` | Settings → Secrets → Actions | o JSON de uma service account com BigQuery Data Editor, Job User e Storage Object Admin |
   | variable `BRAVIS_IT_PROJECT` | Settings → Variables → Actions | `zarv-development-94b6` |
   | variable `BRAVIS_IT_DATASET` | idem | `bravis_it` |
   | variable `BRAVIS_IT_BUCKET` | idem | `zarv-development-94b6-bravis-it` |

   Enquanto não existirem, **dezessete testes ficam em `SKIP`** e o job diz
   isso com um `::warning::` — em vez de passar verde em silêncio, que é como a
   lição 3.5 aconteceu na primeira vez.

3. **Um consumidor que não seja este.** Tudo aqui foi achado por um fetcher HTTP
   escrevendo no BigQuery. `from.Files`, `to.Files`, S3 e GCS entraram na
   `v0.20.0` e ainda não têm quem os use de verdade — e a classe 3.6 diz que o
   que não é exercitado é onde o defeito mora.

4. **O `Execute` instala o logger padrão do processo.** Uma biblioteca que
   chama `slog.SetDefault` decide pelo programa que a importa. Hoje é
   conveniente — um fetcher ganha log estruturado sem escrever nada — e não
   incomodou ninguém, mas é uma decisão que o consumidor não tomou. Vale rever
   antes da `v1.0.0`.
