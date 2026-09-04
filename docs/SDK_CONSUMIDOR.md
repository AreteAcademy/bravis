# O SDK visto pelo primeiro consumidor

**Vale para** `sdk/v0.25.1` · **Escrito em** 2026-09-04 · **Atualizado em** 2026-09-04 · **Revisado em** 2026-09-04

Registro do que o consumidor `zarv-data-pipeline` achou entre 2026-09-02 e
2026-09-04, e do que mudou no SDK por causa disso. **34 versões em três dias**,
e o módulo mudou de nome no meio (`bravis` → `brevis`, na `v0.25.0`).

**Os onze defeitos estão fechados.** O último, o §3 do
[`SDK_V9.md`](SDK_V9.md), na `v0.24.0`.

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

## 2.1 O segundo consumidor: o `gabriel`

Depois do exemplo, um vendor de verdade foi refatorado de Python para Go: o
`gabriel`, que traz ocorrências e alimenta três cubos gold de risco geográfico.
A prova de refatoração bem-sucedida foi a mesma dos dois casos: **o dbt não mudou
uma linha**, porque o `ingestion_id` bate com o que o Python gravava — conferido
contra linhas reais de 2020 tiradas da tabela.

Ele exercitou partes do SDK que o exemplo não tocava, e achou três coisas:

- **O `Pipeline` cobre a forma de um endpoint, não a de N.** Um vendor que faz
  ~56 requisições em lote sobre coordenadas vindas do warehouse não cabe em
  `sdk.Run(Pipeline{})`. Cabe no SDK: `Extract`, `Transform` e `Load` são
  exportados e o `main` faz o laço. Esse caminho nunca tinha sido usado.
- **Paginação por número de página não existe.** O `gabriel` pagina com
  `page=1,2,3…`, e o SDK só tem offset e cursor — o consumidor escreveu
  `OffsetKey: "page", PageSize: 1`, onde `PageSize` não é o tamanho da página e
  sim o incremento. Funciona, e é um truque.
- **O erro do staging não diz o que fazer.** Acima de 5000 linhas a carga encena
  por GCS, e sem bucket ela falha com `The specified bucket does not exist` — sem
  dizer qual bucket, que o nome padrão mudou na `v0.25.0`, nem as duas saídas.

E achou uma quarta, que não é do SDK e é a mais séria: **dois terços do fetcher
(183 de 284 linhas) não eram sobre o Gabriel** — eram sessão, cookie e
armazenamento de credencial. Está medido e proposto em
[`plan/2026-09-04-sdk-http-autenticacao.md`](plan/2026-09-04-sdk-http-autenticacao.md).

## 2.2 A credencial: uma classe nova, e a pior

O `gabriel` guardava o cookie de sessão numa tabela do BigQuery. Três coisas
erradas, em ordem crescente de gravidade:

1. um warehouse **analítico** guardando estado de sessão, que não se analisa;
2. no dataset `bronze`, cujo significado é "dado bruto do fornecedor" — a camada
   errada dentro do banco errado;
3. **eram credenciais vivas**, onze delas, legíveis por qualquer `dataViewer` do
   dataset. E não credenciais de serviço: a resposta de `/api/auth/session` traz
   `user: {name, email, image, id}`. **A pipeline se autentica como uma pessoa
   do time.** Quem lesse o token agiria como ela no sistema do fornecedor.

O terceiro ponto não apareceu por análise de segurança: apareceu porque alguém
perguntou *"como essa sessão é criada e por quem?"*. A pergunta certa achou o
que nenhuma revisão de código tinha achado.

A correção foi tirar o armazenamento inteiro, não movê-lo. A credencial passou a
vir de env var, e **no lugar do store entrou um aviso**: a resposta da renovação
traz a validade, e o fetcher a reporta a cada execução, com `WARN` a sete dias do
vencimento. Um store adia o vencimento; um aviso o resolve.

Antes de decidir, o pressuposto foi testado contra a API — sem store, o valor
rotacionado é descartado, e isso só funciona se o token antigo sobreviver à
rotação:

```
1. GET /auth/session com o token atual   ->  Set-Cookie com token NOVO
2. GET /occurrences com o ANTIGO         ->  HTTP 200
```

Sobrevive. O custo passou a ser único e conhecido — alguém recola a env por mês —
em vez de uma credencial pessoal replicada num dataset de análise.

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

### 3.5 Estado que não é dado, guardado onde o dado mora

O `gabriel` escrevia o cookie de sessão no BigQuery porque era o armazenamento
que estava à mão — o fetcher já tinha um cliente conectado. Ninguém decidiu
"vamos guardar credencial no warehouse"; a decisão foi tomada pela conveniência.

> O teste é perguntar do que o dado **serve**. Se ninguém vai analisá-lo, ele
> não pertence a um banco analítico — por mais perto que o cliente esteja.

Vale para além de credenciais: cursores de paginação, marcas d'água de
incremental, checkpoints. Tudo isso é estado de execução, e todo ele tem a mesma
tentação.

### 3.6 O teste existia e nunca tinha rodado

`TestIntegrationMergeDoesNotDouble` cobria exatamente o §1 — tabela ausente,
`CreateTable`, `DedupMerge` — desde a `v0.2.1`. Nunca rodou: `requireIntegration`
pula sem `BREVIS_IT_PROJECT`, e a variável não estava definida em nada
automatizado. Quando finalmente rodou, achou **quatro** defeitos a mais numa
única execução, incluindo `Load` mutando a fatia do chamador — que quebrava
exatamente o retry que o `DedupMerge` existe para tratar.

> O defeito não escapou por falta de teste. Escapou porque o teste estava atrás
> de uma variável de ambiente.

### 3.7 O teste cobria só a forma fácil

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

O exemplo `open_meteo/hourly_temperature` foi aposentado quando cumpriu o papel.
O fetcher ativo é o `gabriel`, e ele lê assim:

```go
origem := &from.HTTP{
    URL: ".../occurrences?limit=1000&skipCount=true&page=1",
    OffsetKey: "page", PageSize: 1, MaxPages: 200, DataKey: "data",
}

sdk.Run(sdk.Pipeline{
    Before: func(ctx context.Context, _ *sdk.Pipeline) error {   // credencial e validade
        cookie, err := prepararSessao(ctx); …
        origem.Header = map[string][]string{"Cookie": {cookie}, …}
        return nil
    },
    Source:    sdk.Source{From: origem},
    Transform: []sdk.Transformer{ … , sdk.IngestionID(…), sdk.IngestionLoadedAt(),
                                  sdk.Accept(as seis colunas) },
    Target:    sdk.Target{To: bigquery.Table{…}, Columns: […]},
})
```

Quatro coisas que só um consumidor real produz, e que valem para o próximo:

- **`Accept` no fim, e não `Without`.** As chaves de uma ocorrência variam entre
  registros, então não há lista fixa para descartar. `Accept` nomeia o que fica.
- **A cadeia é duplicada no teste, de propósito.** Um teste que a importasse do
  `main` não pegaria uma **reordenação** — e a ordem é o que decide se o
  `IngestionID` encontra os campos que lê.
- **Os valores esperados vêm da tabela**, não do código. Gerá-los com a mesma
  implementação provaria só que ela concorda consigo mesma.
- **`InlineLimit` explícito.** O padrão encena por GCS acima de 5000 linhas, e
  este vendor tem ~11,4 mil; não há bucket de staging, e o vendor em Python nunca
  precisou de um. O comentário diz o que fazer quando a coleção passar do novo
  limite: criar o bucket, não subir o número de novo.

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
   | variable `BREVIS_IT_PROJECT` | Settings → Variables → Actions | `zarv-development-94b6` |
   | variable `BREVIS_IT_DATASET` | idem | `bravis_it` |
   | variable `BREVIS_IT_BUCKET` | idem | `zarv-development-94b6-bravis-it` |

   Enquanto não existirem, **dezessete testes ficam em `SKIP`** e o job diz
   isso com um `::warning::` — em vez de passar verde em silêncio, que é como a
   lição 3.5 aconteceu na primeira vez.

3. **Um consumidor que não seja este.** Tudo aqui foi achado por um fetcher HTTP
   escrevendo no BigQuery. `from.Files`, `to.Files`, S3 e GCS entraram na
   `v0.20.0` e ainda não têm quem os use de verdade — e a classe 3.7 diz que o
   que não é exercitado é onde o defeito mora.
4. **A autenticação**, que é o único trabalho grande ainda por fazer:
   [`plan/2026-09-04-sdk-http-autenticacao.md`](plan/2026-09-04-sdk-http-autenticacao.md).
   Quatro dos 24 vendors do consumidor precisam dela, e cada um a reimplementa.

4. **O `Execute` instala o logger padrão do processo.** Uma biblioteca que
   chama `slog.SetDefault` decide pelo programa que a importa. Hoje é
   conveniente — um fetcher ganha log estruturado sem escrever nada — e não
   incomodou ninguém, mas é uma decisão que o consumidor não tomou. Vale rever
   antes da `v1.0.0`.
