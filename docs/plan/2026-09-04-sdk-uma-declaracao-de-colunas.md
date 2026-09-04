# Uma declaração de colunas, no formato do DDL

**Escrito em** 2026-09-04 · **Base** `sdk/v0.17.1` · **Alvo** `sdk/v0.18.0`
(quebra compatibilidade)

Pedido de quem consome o SDK:

> Ainda tem algo que está me incomodando, não está clara a estrutura de dados.
> O `Source` precisa ter somente os dados de config e ativar o metadado para
> conseguir as colunas `ingestion_*`. O `Transform` precisa ter o resto dos
> dados, usar o `Records` para validar, e o schema precisa estar explícito sem
> quebrar em 2 partes.

A referência é o DDL que o próprio consumidor mantém, e ele é claro de primeira:

```sql
CREATE TABLE IF NOT EXISTS bronze.vendors_open_meteo_hourly_temperatures (
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

Seis colunas, um bloco, ordem visível. **O fetcher que escreve nessa tabela não
tem nada parecido**, e é isso que esta spec conserta.

---

## 1. A promessa da `v0.15.0` não se cumpriu no primeiro consumidor real

O commit que introduziu `sdk.Schema` disse:

> Posto por último na cadeia, ele responde numa linha a pergunta que antes não
> tinha resposta em código nenhum — que colunas essa tabela tem.

No fetcher de verdade, a resposta está em **três** lugares e nenhum deles é
completo:

```go
sdk.Schema("time", "temperature_2m", "latitude", "longitude"),   // (a)
...
sdk.Schema("provider", "entity", "payload", "source_key"),       // (b)
...
Target.Metadata = &sdk.Metadata{Provider: …, Entity: …, …}        // (c)
```

- **(a) não são colunas da tabela.** É o que o fetcher aceita da fonte; nenhum
  desses quatro nomes existe no destino.
- **(b) são quatro das seis.**
- **(c) produz as outras duas e não as nomeia.** `ingestion_id` e
  `ingestion_loaded_at` não aparecem escritos em lugar nenhum do fetcher —
  exatamente a situação que a `v0.15.0` existiu para acabar, só que agora com
  duas colunas em vez de seis.

E o pior detalhe: **`Schema` aparece duas vezes querendo dizer duas coisas
diferentes.** Quem lê não tem como saber qual das duas é a tabela. É a mesma
crítica que o SDK fez ao substituir o `Only` — "dois nomes para quase a mesma
coisa também é o oposto de claro" — de cabeça para baixo: um nome para duas
coisas diferentes.

---

## 2. As duas verificações são legítimas. O nome não.

Não junte (a) e (b) numa só chamada. Elas checam coisas diferentes, e as duas
valem:

| | pergunta | quando falha |
|---|---|---|
| (a) | a fonte ainda manda os campos que eu leio? | o Open-Meteo para de mandar `temperature_2m` → erro nomeando o campo, em vez de um `payload` silenciosamente incompleto |
| (b) | a linha tem as colunas do destino? | o fetcher esquece de compor `provider` → erro antes da carga |

Perder (a) para ter "um só schema" trocaria clareza por um buraco de detecção. O
conserto é **de nomenclatura e de completude**, não de fusão:

- **Só (b) se chama `Schema`**, e passa a listar as **seis**.
- **(a) recupera um nome próprio**, porque é uma etapa de moldagem e não a
  declaração do destino.

---

## 3. As três mudanças

### 3.1 `Schema` sai do `Transform` e vira a declaração do destino

Hoje `Schema` é um `Transformer`, e por isso não pode nomear
`ingestion_id`/`ingestion_loaded_at`: o SDK só os acrescenta em
`load.go:200-210`, **depois** de todo o `Transform`. Um `Schema` que os nomeasse
falharia com "the schema names ingestion_id, which this record does not have".

É por isso que a lista está incompleta — não por descuido de quem escreveu o
fetcher, mas porque a etapa em que ela vive não conhece as duas colunas ainda.

Então mova a declaração para onde a informação toda existe: o destino.

```go
Target: sdk.Target{
    Dataset: "bronze",
    Table:   "vendors_open_meteo_hourly_temperatures",

    // A tabela, na ordem do DDL. Inclui as que o SDK preenche.
    Columns: []string{
        "ingestion_id",         // do bloco Metadata
        "ingestion_loaded_at",  // do bloco Metadata
        "provider",
        "entity",
        "source_key",
        "payload",
    },

    Metadata: &sdk.Metadata{Provider: provider, Entity: entity, Key: …, When: …},
},
```

A verificação passa a rodar sobre a linha **final** — o que o `Transform`
produziu mais o que o `Metadata` preencheu — e erra nomeando qualquer coluna que
nenhum dos dois entregou, e qualquer campo que a linha traz e a lista não
declara. A `reconcile` já faz metade disso contra a tabela real; a diferença é
que agora existe uma **declaração** para conferir, e ela está no fetcher.

`Columns` em `Target` porque é a forma da tabela, e é ali que o dataset e o nome
dela já estão. (A alternativa é `Pipeline.Schema`; se ficar mais legível ao lado
do `Transform`, escolha essa — o que não pode continuar é a lista incompleta
dentro da cadeia de transformação.)

### 3.2 (a) recupera um nome próprio

A etapa que declara o que se aceita da fonte precisa de um nome que não seja
`Schema`. `Only` está livre — foi removido na `v0.15.0` por **descartar campo
ausente em silêncio**, e esse defeito não volta se ele erra no ausente, que é o
comportamento do `Schema` de hoje.

```go
Transform: []sdk.Transformer{
    sdk.Only("time", "temperature_2m", "latitude", "longitude"),   // da fonte
    sdk.Compute("payload", …),
    sdk.Compute("provider", …),
    sdk.Compute("entity", …),
    sdk.Compute("source_key", …),
},
```

O nome final é seu; o requisito é que ler a cadeia não deixe dúvida sobre qual
linha é a tabela — e a resposta passa a ser "nenhuma, a tabela está no `Target`".

### 3.3 `Records` sai do `Source`

`Source.Records` é a única coisa em `Source` que não é config: URL, `Header`,
`Timeout`, `TotalTimeout`, retry, paginação, `Format` e `Preview` são todos
ajuste; `Records` é a regra de negócio que decide o que uma resposta significa.

Suba para `Pipeline.Records`, ao lado de `Transform` — que também roda sobre o
extraído e também mora no `Pipeline`. O `Source` fica config puro, que é como o
consumidor descreveu o que espera dele.

Mantenha a recusa de `Records` junto de `DataKey`: os dois dizem onde estão os
registros.

---

## 4. Como o fetcher passa a ler

```go
sdk.Run(sdk.Pipeline{
    Name: "open_meteo/hourly_temperature",

    // config
    Source: sdk.Source{URL: …, Header: …, Timeout: …, TotalTimeout: …},

    // o que uma resposta significa
    Records: func(r sdk.Response) ([]any, error) { … },

    // a linha
    Transform: []sdk.Transformer{sdk.Only(…), sdk.Compute(…), …},

    // o destino, e as colunas dele
    Target: sdk.Target{Dataset: …, Table: …, Columns: […], Metadata: &sdk.Metadata{…}},
})
```

Quatro perguntas, quatro lugares: **de onde vem, o que significa, que linha
monta, para onde vai e com que colunas.** Hoje a última está partida em três.

---

## 5. O que não fazer

- **Não** funda (a) e (b) para ter uma chamada só. §2 — as duas verificações
  pegam coisas diferentes.
- **Não** deixe `Columns` opcional com o comportamento antigo de reserva. Um
  opcional que, ausente, volta ao `Schema` na cadeia mantém os dois caminhos
  vivos e a dúvida de pé.
- **Não** infira `Columns` da tabela real. Ela existe para ser **conferida**
  contra a tabela; derivá-la de lá a torna uma cópia que sempre concorda.
- **Não** mova `Metadata` para o `Transform`. Ele configura o `ingestion_id` e o
  layout da tabela criada; o que faltava era as colunas dele **aparecerem** na
  declaração, e o §3.1 resolve isso.

---

## 6. Critério de pronto para a `sdk/v0.18.0`

1. Uma única declaração lista as colunas do destino, incluindo as que o SDK
   preenche, e ela **não** vive na cadeia de `Transform`.
2. Coluna declarada que nem o `Transform` nem o `Metadata` entregam é erro
   nomeando a coluna, antes da carga.
3. Campo que a linha traz e a declaração não lista é erro nomeando o campo — a
   assimetria que a `reconcile` já usa, e pelo mesmo motivo.
4. A declaração é conferida contra a tabela real, e a divergência nomeia a
   coluna e os dois lados.
5. A etapa "o que aceito da fonte" tem nome próprio, e continua errando em campo
   ausente.
6. `Records` é campo de `Pipeline`. `Source` não tem mais nenhum campo que não
   seja config.
7. `Records` junto de `DataKey` segue recusado.
8. O `CHANGELOG` traz o diff de migração do fetcher do consumidor por extenso —
   é o que ele vai copiar.
9. `go test ./... -short` verde, `go vet ./...` limpo, e os `examples/`
   atualizados: o `08-fetcher-minimo` é o primeiro que alguém lê.

---

## 7. A prova

Ponha o DDL do topo desta spec ao lado do fetcher e pergunte a alguém:

> Estas duas coisas descrevem a mesma tabela?

Hoje é preciso ler três lugares, saber que `Metadata` produz duas colunas sem
nomeá-las, e descobrir que um dos dois `Schema` não é sobre a tabela. Quando a
resposta for imediata, esta spec está cumprida.
