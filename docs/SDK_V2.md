# SDK v2 — levar a lógica para dentro

**Aberto em** 2026-09-02 · **Base** `sdk/v0.2.1` · **Alvo** `sdk/v0.3.0`

Prompt de execução. O objetivo é uma API em que escrever um fetcher seja
**declarar o que se quer**, não orquestrar chamadas:

```go
dados, err := sdk.Extract(ctx, fonte)
res, err := sdk.Load(ctx, dados, destino)
```

Duas chamadas. Tudo que estiver entre elas hoje e não for específico do
fornecedor tem de mudar de lado.

---

## 1. A medição que justifica isto

O primeiro consumidor real do SDK é
`zarv-data-pipeline/scripts/vendors/exemplo_go`. Ele roda contra o BigQuery de
dev, extrai 24 leituras do Open-Meteo e carrega. Contado:

| bloco | linhas de código |
|---|---|
| flags e `main` | 16 |
| orquestração, config, `LoadConfig`, logging | 48 |
| montar a `Fonte`, guarda, iterar o `Seq2`, embrulhar erro | 37 |
| **lógica do fornecedor** (expandir dois arrays paralelos) | **37** |
| **total** | **156** |

**Só 24% do arquivo é sobre o Open-Meteo.** Os outros 76% se repetem, idênticos,
em qualquer fetcher — e vão se repetir 24 vezes se os 24 vendors Python migrarem.

O alvo é inverter essa proporção. Um fetcher novo deveria ser: a URL, como
identificar uma leitura, e o que fazer com o formato do fornecedor.

---

## 2. Antes e depois

O que o exemplo tem hoje, resumido:

```go
projeto := os.Getenv("GOOGLE_PROJECT_ID")
if projeto == "" { return fmt.Errorf("GOOGLE_PROJECT_ID é obrigatória") }

linhas, err := extract.JSON(ctx, sdk.Fonte{URL: url, Header: ..., Timeout: ..., Guard: ...})
var envelopes []sdk.Envelope
for env, err := range linhas {
    if err != nil { return err }
    lote, err := expandir(env.Payload)     // lógica do vendor
    if err != nil { return err }
    envelopes = append(envelopes, lote...) // e aqui cada um preenche
}                                          // Provider/Entity/SourceKey/RecordTS
                                           // à mão, item por item

carregador, err := load.New(ctx, &sdk.LoadConfig{ProjectID: projeto, Dataset: ..., Table: ..., AddMetadata: true})
if err != nil { return err }
res, err := carregador.Load(ctx, envelopes...)
```

O que se quer:

```go
dados, err := sdk.Extract(ctx, sdk.Fonte{
    URL:     "https://api.open-meteo.com/v1/forecast?...",
    Formato: sdk.JSON,
    Guarda:  sdk.RecusarSe("error"),      // 200 que não é dado, declarativo
    Expandir: sdk.ArraysParalelos("hourly", "time", "temperature_2m"),
})

res, err := sdk.Load(ctx, dados, sdk.Destino{
    Provider: "open_meteo",
    Entity:   "hourly_temperature",
    Chave:    sdk.Chave("latitude", "longitude", "time"),
    Quando:   sdk.Campo("time"),
})
```

O `Expandir` é ambicioso e é opcional: quando o formato do fornecedor não couber
num helper, o consumidor continua podendo mapear à mão. **O caso difícil tem de
continuar possível** — o que não pode é o caso comum exigir 119 linhas.

---

## 3. O que muda de lado

### 3.1 Config e ambiente — o SDK lê, com precedência escrita

Hoje cada consumidor faz `os.Getenv("GOOGLE_PROJECT_ID")` e valida. Passa a ser
do SDK, com precedência **documentada e nessa ordem**:

1. o que o consumidor passou explicitamente na struct;
2. variável de ambiente;
3. default do SDK;
4. erro, se não houver default sensato.

Variáveis mínimas: projeto, dataset, bucket de staging, nível de log. Nomeie com
prefixo (`BRAVIS_SDK_*`) **exceto** as que já são padrão do ecossistema
(`GOOGLE_PROJECT_ID`, `GOOGLE_APPLICATION_CREDENTIALS`) — inventar um nome novo
para algo que já tem nome é atrito.

> **A armadilha:** ler ambiente escondido faz o código funcionar na máquina de
> quem escreveu e falhar no pod. O SDK precisa **logar, no início, de onde veio
> cada valor** — `projeto=x (de GOOGLE_PROJECT_ID)`. Sem isso, "por que ele
> escreveu no dataset errado?" vira uma hora de investigação.

### 3.2 Schema — o SDK passa a ser dono

Hoje `Load` verifica a tabela e **recusa criá-la** ("Create it manually with your
desired schema"). Isso muda: o SDK cria.

Isto **resolve a pergunta aberta na §5 do `SDK_LOAD.md`** — quem produz as seis
colunas da landing. A resposta passa a ser: o SDK, por padrão.

```sql
CREATE TABLE IF NOT EXISTS <dataset>.vendors_<provider>_<entity>s (
  ingestion_id        STRING NOT NULL,
  ingestion_loaded_at TIMESTAMP NOT NULL,
  provider            STRING NOT NULL,
  entity              STRING NOT NULL,
  source_key          STRING,
  payload             JSON   NOT NULL
)
PARTITION BY DATE(ingestion_loaded_at)
CLUSTER BY provider, entity;
```

Três requisitos, e o terceiro é o que evita um incidente:

- **Colunas de primeiro nível**, não `_bravis_*` dentro do payload. O
  `metadata_vendor()` do dbt lê `ingestion_id`, `provider`, `entity`,
  `source_key`, `ingestion_loaded_at` e `payload` como colunas. Hoje o SDK dobra
  tudo para dentro do payload e nada disso é legível a jusante.
- **Particionamento e clustering vêm de fábrica.** Uma landing sem partição custa
  varredura inteira em todo MERGE do bronze — foi medido no consumidor:
  `bronze_id_verification` gastava 58,96 GiB de MERGE contra 0,0 GiB de SELECT.
- **Nunca alterar destrutivamente.** `CREATE IF NOT EXISTS` e, para tabela que já
  existe, **comparar e recusar** com um erro que diz a diferença. Um SDK que faz
  `ALTER`/`DROP` sozinho é um SDK capaz de apagar histórico. O modo agnóstico
  atual (`Table` explícita, schema livre) continua disponível para quem não quer
  o contrato.

### 3.3 Mapeamento para Envelope — declarativo

É a maior fonte de repetição hoje: cada consumidor preenche `Provider`, `Entity`,
`SourceKey` e `RecordTS` item por item.

```go
Chave:  sdk.Chave("latitude", "longitude", "time")  // campos do payload → source_key
Quando: sdk.Campo("time")                           // campo do payload → record_ts
```

`Chave` concatena os campos numa ordem estável. **O separador e a ordem entram no
`ingestion_id`**, então precisam ser fixados e documentados na v0.3.0 e nunca
mais mudados — mudá-los faz a mesma leitura entrar duas vezes. Escreva isso na
doc da função, não só aqui.

Quando o campo não existir no payload, **erro** com o nome do campo e as chaves
disponíveis. Silenciar produz `source_key` vazio, que o SDK já rejeita — mas com
uma mensagem que não diz qual campo faltou.

### 3.4 Paginação — implementar, ou remover da struct

`CursorKey`, `OffsetKey` e `PageSize` existem em `types.go` desde a v0.1.0 e são
lidos em **nenhum** lugar. Campo público que não faz nada é pior que campo
ausente: quem o preenche acredita ter configurado algo.

Três formas cobrem quase tudo:

| forma | como |
|---|---|
| cursor | campo na resposta com o token da próxima página |
| offset/limit | dois parâmetros na query |
| `Link` | cabeçalho RFC 5988, `rel="next"` |

Requisitos que não são óbvios e custam caro se faltarem:

- **teto de páginas**, configurável, com erro ao estourar. Um cursor que não
  avança é laço infinito, e API de governo faz isso;
- **detecção de cursor repetido** — mesmo token duas vezes é fim, não é próxima;
- **streaming**: página N não pode esperar a página N+1. Devolva `iter.Seq2` e
  emita conforme chega, senão paginação e memória se cancelam.

### 3.5 Load estratégico e com deduplicação

Aqui há uma **decisão de produto** a tomar antes de codar, porque muda o que o
SDK promete.

Hoje o SDK anexa e a documentação diz que idempotência é responsabilidade do
bronze. Medido no consumidor: duas execuções da mesma janela deram **48 linhas
para 24 `ingestion_id` distintos**.

Três formas de deduplicar, com o custo real de cada uma:

| forma | como | custo | pega o quê |
|---|---|---|---|
| **`insertID`** | passa o `ingestion_id` como insertID do streaming | zero | melhor-esforço, janela de ~1 min. Pega o retry imediato; não pega reexecução no dia seguinte |
| **MERGE** | carrega numa temporária e faz `MERGE ... ON ingestion_id` | uma varredura do alvo por carga | pega tudo, e é o que o bronze já faz |
| **append + dedup a jusante** | o que existe hoje | zero | nada no load; tudo no bronze |

**Medido:** o `insertID` funciona — na primeira versão do exemplo, com ele, duas
execuções deram 24 linhas para 24 ids. Sem ele, 48 para 24.

**Recomendação:** `insertID` por padrão (é grátis e pega o caso comum, que é
retry) e `MERGE` como opção explícita, porque ele custa varredura e ninguém deve
pagar isso sem pedir. **Nunca dedup silencioso que custe dinheiro.** E diga no
`LoadResult` qual foi usada, senão a telemetria mente de novo.

Sobre "estratégico": a escolha inline/GCS já existe. O que falta é ela ser
**honesta** — os formatos CSV e Parquet ainda são aceitos e ignorados (§3 do
`SDK_LOAD.md`). Ou implementa, ou recusa no `New`.

### 3.6 O que eu acrescentaria, e por quê

Coisas que o consumidor vai construir sozinho, mal, se o SDK não trouxer:

**Um `Resultado` que sirva de log.** Linhas, bytes, estratégia, dedup, duração,
páginas buscadas, tentativas gastas. O consumidor imprime um `slog.Info` e acabou
a observabilidade dele. É o que torna a comparação com o Python possível sem
instrumentação extra.

**Erro tipado com o que fazer.** `ErroDeFonte` (a API falhou), `ErroDeFormato`
(veio, mas não parseia), `ErroDeDestino` (BigQuery recusou). São três ações
diferentes de plantão: esperar, corrigir o parser, olhar o schema. Um `error`
plano obriga a ler a mensagem para decidir.

**Redação de segredo no log.** Chave de API em query string é o caso comum
(FIRMS, WAQI). Se o SDK loga a URL, ele **tem** de redigir — vazar chave em log
de pod é incidente, e é o tipo de coisa que ninguém percebe até o log ir para
fora.

**`--dry-run` de fábrica.** Extrai, mapeia, imprime os primeiros N envelopes com
o `ingestion_id`, e **não escreve**. Todo fetcher precisa disso no primeiro dia e
todo mundo reescreve.

**Um `Pipeline` opcional para o caso trivial.** Quando não há expansão, o fetcher
inteiro vira uma struct e um `sdk.Rodar(ctx, p)` — que já cuida de flags,
`-dry-run`, logging e código de saída. É o que faz um vendor novo caber em 30
linhas.

---

## 4. As armadilhas já pagas — não regridam

Cada uma custou uma investigação. Estão aqui para não voltarem.

| armadilha | onde apareceu |
|---|---|
| **`ingestion_id` é contrato** | UUID v5 sobre `namespace` e `provider\|entity\|source_key\|record_ts`. Conferido contra o `uuid.uuid5` do Python: os UUIDs batem. Qualquer mudança em separador, ordem ou formato de `record_ts` faz a linha do Go não casar com a do Python |
| **`StructSaver` não aceita dado dinâmico** | `StructSaver{Struct: json.RawMessage(...)}` fez o cliente recusar em runtime. Para linha cuja forma só se conhece em runtime, o caminho é `ValueSaver` sobre `map[string]bigquery.Value` |
| **`SourceFormat` vazio é CSV** | `NewGCSReference` não assume o formato do arquivo. NDJSON carregado sem `SourceFormat` falha |
| **Campo público sem implementação** | seis deles na v0.1.1. Quem preenche acredita ter configurado |
| **Telemetria que mente** | `LoadResult.Format` reportava o configurado, não o escrito. Número errado é pior que número ausente, porque ninguém desconfia |
| **`ErrorRows` nunca preenchido** | declarado, sempre `nil`, e o README mandava lê-lo depois da falha — o trecho documentado causava panic |
| **Publicar sem compilar** | a v0.1.0 foi ao proxy sem nunca ter sido buildada. `go build ./...` e `go vet ./...` no CI, antes da tag |

---

## 5. O que **não** deve entrar

Um SDK que faz tudo vira um framework, e framework não se adota — se herda.

- **Transformação.** O SDK extrai e carrega. Transformar é do dbt.
- **Agendamento.** É do Bravis, não da biblioteca.
- **Outros destinos** (Postgres, S3) enquanto não houver um segundo consumidor
  real pedindo. Interface para permitir depois, sim; implementação agora, não.
- **Mágica não substituível.** Toda escolha automática do SDK — estratégia,
  formato, dedup, schema — tem de ser sobrescrevível por um campo explícito, e o
  `Resultado` tem de dizer o que foi escolhido.

---

## 6. Critério de pronto

1. O `exemplo_go` do consumidor cai de **156 para menos de 60 linhas** de código,
   com as ~37 de lógica do fornecedor preservadas. É a métrica desta versão.
2. Um fetcher **novo**, sem expansão, cabe em menos de 30 linhas.
3. Config lê de ambiente com precedência documentada e **loga de onde veio**.
4. `Load` cria a tabela de landing com as seis colunas, particionada e
   clusterizada, e **recusa** alterar tabela existente que difira.
5. Paginação implementada nas três formas, com teto de páginas e detecção de
   cursor repetido — ou os campos saem da struct.
6. Dedup por `insertID` por padrão, `MERGE` opcional, e o `Resultado` diz qual
   foi usada.
7. Nenhum campo público sem implementação. Nenhum valor no `Resultado` que não
   descreva o que aconteceu de fato.
8. `go build ./...` e `go vet ./...` verdes no CI **antes** da tag.
9. Um teste de integração real (`testing.Short()`) que cria a tabela, carrega,
   recarrega o mesmo lote e confere que a contagem **não** dobrou.
10. O `exemplo_go` reescrito com a API nova, rodado contra dev, com o número de
    linhas antes e depois no README dele.
