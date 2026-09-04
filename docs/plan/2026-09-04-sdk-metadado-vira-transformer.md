# As duas colunas de metadado viram transformers, e o bloco desaparece

**Escrito em** 2026-09-04 · **Base** `sdk/v0.23.0` · **Alvo** `sdk/v0.24.0`
(quebra compatibilidade)

Pedido de quem consome o SDK:

> `"ingestion_id"`, `"ingestion_loaded_at"` — podemos fazer essa mudança aqui,
> caso o cliente não use o metadado ativo: `sdk.IngestionId()`,
> `sdk.IngestionLoadAt()`, e simplesmente ignorar a existência do metadado. O
> `AutoID` não vai ajudar muito, ele ficou confuso. E no transformer podemos
> usar para adicionar os valores.

**Faz sentido, e é a conclusão do caminho que o SDK já percorreu.** A regra que a
`v0.15.0` estabeleceu e a `v0.18.0` completou é uma só:

> As colunas são compostas no `Transform`, e o SDK não inventa nenhuma.

O bloco `Metadata` é **a última exceção a essa regra** — e a documentação dele
admite isso em voz alta (`metadata.go:19`):

> Everything else about the row comes from Transform. This block is the one
> exception, and it is opt-in.

Esta spec remove a exceção.

---

## 1. A evidência de que a exceção custa

Quem consome o SDK perguntou **duas vezes** o que o bloco faz, e a segunda vez
depois de uma resposta detalhada:

> "Metadado deve ser ligado ou desligado?"

O §8 do [`SDK_V9.md`](../SDK_V9.md) registra por quê: um "interruptor" com quatro
campos obrigatórios não é um interruptor, e há **três** estados onde a API sugere
dois. O `AutoID` foi a tentativa de dar um estado simples e virou o terceiro
motivo de confusão.

E há um custo estrutural, não só de entendimento: **`Target.Columns` precisa
tolerar duas colunas que o `Transform` nunca produz.** Elas são estampadas depois,
no load (`load.go:193`), então a conferência declarado-vs-linha tem um caso
especial embutido. Com transformers, a linha que chega ao `Columns` está completa
e a conferência não precisa saber de nada.

---

## 2. A forma proposta

Dois transformers, usados como qualquer outro:

```go
Transform: []sdk.Transformer{
    sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
    sdk.Compute("payload", …),
    sdk.Compute("provider", …),
    sdk.Compute("entity", …),
    sdk.Compute("source_key", sdk.Key("latitude", "longitude", "time")),

    // As duas colunas de metadado, no mesmo lugar que as outras quatro.
    sdk.IngestionID("provider", "entity", "source_key", "time"),
    sdk.IngestionLoadedAt(),

    sdk.Without("time", "temperature_2m", "latitude", "longitude"),
},
Target: sdk.Target{
    To:      bigquery.Table{Dataset: "bronze", Name: "vendors_open_meteo_hourly_temperatures"},
    Columns: []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload"},
    // sem Metadata
},
```

Ler a cadeia passa a responder a pergunta inteira: **seis `Compute`/helpers, seis
colunas.** Não sobra nada acontecendo fora dela.

### `sdk.IngestionID(campos ...string)`

Escreve `ingestion_id` com a fórmula congelada de `core/types.go:46` —
`uuid_v5(ns, "provider|entity|source_key|record_ts")`. **Continua sendo a única
implementação**, e é isso que faz esta proposta viável: a razão de não calcular o
id no fetcher sempre foi que uma linha em Go tem de casar com a mesma linha em
Python. Um transformer do SDK preserva o dono único; um `fmt.Sprintf` no fetcher
não.

Os quatro componentes vêm de **campos do registro**, nomeados na chamada:

```go
sdk.IngestionID()                                              // provider, entity, source_key, record_ts
sdk.IngestionID("provider", "entity", "source_key", "time")    // quando os nomes diferem
```

Sem argumentos, lê os quatro nomes canônicos. Campo nomeado e ausente é **erro
nomeando o campo**, como o `Accept` — e é o erro certo: ele significa que a
cadeia está fora de ordem, ou que o `Without` correu antes.

### `sdk.IngestionLoadedAt()`

Escreve `time.Now().UTC()` em RFC 3339. Sem argumentos, porque não há nada para
configurar.

### Sobre os nomes

O pedido escreve `IngestionId` e `IngestionLoadAt`. Duas correções pequenas e que
vale fazer agora, porque nome de API não se troca depois de barato:

- **`IngestionID`**, não `IngestionId` — inicialismo em Go é maiúsculo, e o resto
  do SDK já segue isso (`IngestionID()` no `Envelope`, `ErrorRows`, `URL`).
- **`IngestionLoadedAt`**, não `IngestionLoadAt` — a coluna é
  `ingestion_loaded_at`. Um helper cujo nome não bate com a coluna que ele
  escreve é uma armadilha pequena e permanente.

---

## 3. O que sai, e o que precisa de resposta antes

`Metadata` some, e com ele `AutoID`. Quatro lugares dependem dele hoje:

| onde | o que decide | o que fazer |
|---|---|---|
| `load.go:119` | `DedupMerge requires Metadata` | vira **`DedupMerge` exige a coluna `ingestion_id`** — precondição melhor, porque é o que o merge de fato usa (`mergeSQL(…, metadataID)`) e é conferível contra `Target.Columns` |
| `table.go:179` | o texto da descrição da tabela | passa a olhar se `ingestion_id` está em `Columns` |
| `load.go:193` | `StampMetadata` estampa as duas | sai; e o erro de colisão que ela dava vem de graça do `Compute`, que já recusa sobrescrever |
| **`table.go:50` e `:122`** | **a criação da tabela com as duas colunas `NOT NULL`** | **é a perda real — ver abaixo** |

### A perda real, e a recomendação

A `v0.16.0` fez o SDK **criar a tabela ele mesmo** quando há metadado, porque
autodetect infere as duas como `NULLABLE` e o BigQuery **recusa apertar** uma
coluna depois:

```
Field ingestion_loaded_at has changed mode from REQUIRED to NULLABLE
```

Sem o bloco, o SDK deixa de saber que aquelas duas colunas são dele, e uma tabela
criada por `CreateTable` sai com as duas `NULLABLE`.

Três saídas, e a recomendação é a primeira:

1. **Aceitar.** Autodetect cria `NULLABLE`; quem quer `NOT NULL` declara a tabela
   — com `CreateSQL` ou fora do SDK. É coerente com a decisão que o
   [`SDK_DECISOES.md`](../SDK_DECISOES.md) §1 já tomou: *o SDK não infere tipo de
   coluna*. Criar uma tabela com garantias que ele deduziu é a mesma sobreposição,
   uma camada acima. E o consumidor que motivou tudo isto **já declara a landing
   em DDL do dbt** — o caminho `CreateTable` é conveniência de primeira execução,
   não o modo de operar um warehouse com dono.
2. Reconhecer os dois nomes na criação e tipá-los. **Não faça**: é um default
   escondido decidindo pela forma da tabela, que é a classe 3.3 do
   [`SDK_CONSUMIDOR.md`](../SDK_CONSUMIDOR.md) e o defeito mais caro desta série.
3. `Columns` carregar tipo e modo. Reintroduz a DSL de schema que a `v0.18.0`
   recusou de propósito.

Se a escolha for a 1, **diga no `CHANGELOG` em voz alta**: quem usa `CreateTable`
com metadado hoje passa a ter duas colunas `NULLABLE`, e o conserto é declarar a
tabela.

---

## 4. O que não fazer

- **Não** mantenha `Metadata` como atalho ao lado dos transformers. Três formas
  de obter duas colunas é o que a spec anterior fechou; duas é o mesmo erro
  menor.
- **Não** faça `IngestionID` inferir os campos "por parecença" (`ts`, `timestamp`,
  `record_ts`, `time`). Nomear é o que torna a dependência visível, e adivinhar é
  como um fetcher passa a depender de um campo que ninguém escreveu.
- **Não** deixe `IngestionLoadedAt` configurável com um instante do chamador.
  `ingestion_loaded_at` é quando a linha foi escrita; um valor de fora o
  transforma em outra coisa com o mesmo nome.
- **Não** mude a fórmula, o namespace, a ordem nem o separador. `core/types.go:35`
  os marca como congelados, e o motivo continua de pé: a paridade com o
  `uuid.uuid5` do Python.

---

## 5. Critério de pronto para a `sdk/v0.24.0`

1. `sdk.IngestionID(campos ...string)` e `sdk.IngestionLoadedAt()` existem como
   `Transformer`, e sem argumentos o primeiro lê os quatro nomes canônicos.
2. `IngestionID` produz **o mesmo id** que o bloco produzia. Teste que compara os
   dois caminhos com a mesma entrada, e um contra os UUIDs conhecidos que já
   existem nos testes de `StampMetadata` — se o id mudar, toda carga anterior de
   todo consumidor deixa de casar.
3. Campo nomeado e ausente é erro nomeando o campo.
4. `Metadata`, `AutoID` e `StampMetadata` removidos. `grep -r Metadata sdk/` não
   acha campo público.
5. `DedupMerge` passa a exigir a coluna `ingestion_id`, com erro que diz isso, e
   a exigência é conferida contra `Columns` quando ele está declarado.
6. Decidido e escrito o §3 (as duas colunas `NULLABLE` na criação por autodetect),
   com a linha correspondente no `CHANGELOG`.
7. Teste de integração: uma tabela declarada com as seis colunas, carregada por
   um fetcher **sem** bloco de metadado, com `DedupMerge` — e o `ingestion_id`
   lido de volta, conferido contra o valor esperado.
8. `examples/` migrados. O `08-fetcher-minimo` é o primeiro que alguém lê, e hoje
   ele abre com o bloco.
9. `go test ./... -short` verde e `go vet ./...` limpo.

---

## 6. A prova

Ponha o DDL da landing ao lado do `Transform` e conte:

```sql
ingestion_id, ingestion_loaded_at, provider, entity, source_key, payload
```

Seis colunas no DDL, seis linhas na cadeia que as produz. **Nada acontecendo
fora dela.** Quando for possível ler o fetcher de cima a baixo sem precisar saber
que existe um bloco que estampa duas colunas depois, esta spec está cumprida.
