# Drivers para o MVP — Postgres, MySQL, Files e Redshift

**Escrito em** 2026-09-04 · **Base** `sdk/v0.17.1` · **Alvo** `sdk/v0.18.0` → `v1.0.0-rc`
(quebra compatibilidade na fase 0, de propósito)

Escopo pedido:

| | drivers |
|---|---|
| **Extract** | Postgres, MySQL, Files |
| **Load** | Postgres, MySQL, Redshift, Files |

Mais os dois que já existem: `http` no extract, `bigquery` no load. **Nove drivers
no total**, contra dois hoje.

---

## 0. O que o `Driver` faz hoje: nada

O campo existe nos dois lados e parece um ponto de despacho. Não é — é uma
validação que recusa tudo que não seja o único driver implementado:

```go
// sdk.go:78
case "", DriverHTTP:
    source.Driver = DriverHTTP
default:
    return nil, fmt.Errorf("extract driver %q is not implemented; use %q", ...)
```

A `Source` é inteiramente moldada a HTTP, e a `LoadConfig` a BigQuery:

| `Source` | `LoadConfig` |
|---|---|
| `URL`, `Method`, `Body`, `Header` | `ProjectID`, `Dataset`, `Table` |
| `Timeout`, `TotalTimeout`, `RetryConfig`, `RateLimiter` | `StagingBucket`, `StagingPrefix`, `ThresholdForGCS` |
| `Records func(Response)`, `Format` | `ClusterBy`, `PartitionExpiration`, `RequirePartitionFilter` |
| `FollowLinks`, `CursorKey`, `OffsetKey`, `PageSize`, `MaxPages`, `DataKey` | `CreateSQL`, `CreateTable`, `KeepStagedFile` |

Nenhum desses campos faz sentido para um `SELECT` no Postgres ou para um
arquivo em `s3://`. E nenhum campo do Postgres — DSN, query, coluna de
watermark, tamanho de lote — existe.

---

## 1. Os dois problemas que nove drivers criam

### 1.1 Campo morto em escala

Enfiar nove drivers nessas duas structs produz umas quarenta opções novas, e
para qualquer driver escolhido a maioria não faz nada. Um `Source` de Postgres
com `FollowLinks` e `RateLimiter`. Um `Target` de arquivo com `ClusterBy` e
`RequirePartitionFilter`.

Este SDK passou a semana inteira consertando exatamente essa classe de defeito:
`ExtraMetadata` que escondia colunas, `applyLayout` escrito e nunca chamado,
três `With*` sem re-export, `MetadataNamespace` aceito e ignorado,
`DeleteAfterLoad` documentado com um default que um `bool` não consegue ter.
**Nove drivers numa struct de união é essa fábrica ligada na tomada.**

### 1.2 Peso de dependência — e este é mensurável

Hoje, medido na `v0.17.1`:

```
pacotes no grafo da raiz do SDK: 458
  só extract:                    189
  só load:                       454   ← BigQuery, Arrow, Thrift
binário de um consumidor que só faz import do sdk: 21 MB
```

A raiz importa `sdk/load`, que importa `cloud.google.com/go/bigquery`. **Quem
quiser fazer Postgres → Postgres compila a pilha inteira do BigQuery.** Com
nove drivers no mesmo pacote isso vira: todo consumidor carrega o driver de
Redshift, o de MySQL e o cliente S3 para usar um só.

Go poda dependência por pacote importado, não por campo usado. A única forma de
não pagar por um driver é **não importar o pacote dele**.

---

## 2. A forma proposta: o driver é o valor, não um enum

Em vez de um enum e uma struct de união, cada driver é um tipo que carrega a
própria configuração e o próprio comportamento:

```go
sdk.Run(sdk.Pipeline{
    From: from.Postgres{
        DSN:   os.Getenv("PG_DSN"),
        Query: "SELECT id, criado_em, valor FROM pedidos WHERE criado_em >= $1",
        Args:  []any{sdk.LogicalDate},
    },

    Transform: []sdk.Transformer{
        sdk.Schema("id", "criado_em", "valor"),
    },

    To: to.Redshift{
        DSN:     os.Getenv("RS_DSN"),
        Table:   "landing.pedidos",
        Staging: "s3://meu-bucket/stage/",
        IAMRole: "arn:aws:iam::...:role/redshift-copy",
    },

    Metadata: &sdk.Metadata{AutoID: true},
})
```

Duas interfaces, em `internal/core`:

```go
// Reader produz registros. Uma implementação por origem.
type Reader interface {
    Read(ctx context.Context, run RunContext) (iter.Seq2[Envelope, error], error)
}

// Writer consome registros. Uma implementação por destino.
type Writer interface {
    Write(ctx context.Context, records []Envelope, opt WriteOptions) (*LoadResult, error)
}
```

E dois subpacotes, que é o que faz a poda de dependência funcionar:

```
sdk/from    HTTP  Postgres  MySQL  Files
sdk/to      BigQuery  Postgres  MySQL  Redshift  Files
```

### Por que subpacotes e não `sdk.FromPostgres`

Três razões, em ordem de peso:

1. **Poda de dependência.** `from.Postgres` importa `pgx`; `to.BigQuery` importa
   o SDK do Google. Quem usa um não compila o outro. Num único pacote, Go não
   tem como separar.
2. **Colisão de nome.** `Postgres`, `MySQL` e `Files` existem nos dois lados com
   configurações diferentes. `from.Postgres` tem `Query`; `to.Postgres` tem
   `Table`. Um tipo só com os dois campos traz de volta o campo morto.
3. **Leitura.** `From: from.Postgres{...}` diz de onde e para onde sem prefixo
   repetido.

O custo é uma linha de import a mais. A alternativa, `sdk.FromPostgres` /
`sdk.ToPostgres`, resolve 2 e 3 mas **não resolve 1**, que é o problema medido.

### O que fica na raiz

Só o que atravessa todos os drivers, e que já está provado:

`Envelope` · `Transform` e os compositores · `Schema` · `Metadata` · `Dedup` ·
`Preview` · `Stats` · `Result` · `RunContext` · `Reject` / `SkipRecord` ·
`Pipeline` e `Run`.

Um fetcher que troca Postgres por MySQL muda uma linha e nada mais.

---

## 3. Driver por driver: o que cada um exige de verdade

### 3.1 `from.Postgres` e `from.MySQL`

```go
from.Postgres{
    DSN:       "postgres://...",
    Query:     "SELECT ... WHERE atualizado_em > $1 ORDER BY atualizado_em, id LIMIT $2",
    Args:      []any{sdk.LogicalDate, 50_000},
    FetchSize: 10_000,   // linhas por ida ao servidor
}
```

**Bibliotecas.** `github.com/jackc/pgx/v5` (nativo, não `database/sql`: dá
`CopyFrom` no load e tipos ricos na leitura) e
`github.com/go-sql-driver/mysql` sobre `database/sql`.

**Streaming é obrigatório.** `Rows` já entrega linha a linha nas duas libs; o
que não pode acontecer é o driver montar `[]Envelope` antes de devolver. O
`iter.Seq2` que o extract já usa mantém isso, e há teste equivalente ao
`TestBodyStreamsFully` que hoje protege o HTTP.

**Mapeamento de tipo — e aqui está a armadilha.** O registro vira JSON, então
cada tipo SQL precisa de uma escolha explícita:

| SQL | Go | JSON | por quê |
|---|---|---|---|
| `NUMERIC` / `DECIMAL` | `string` | string | `float64` perde precisão em dinheiro. Virar string preserva, e quem quiser número converte no `Transform` |
| `TIMESTAMPTZ` | `time.Time` | RFC 3339 | |
| `DATE` | `time.Time` | `YYYY-MM-DD` | sem hora falsa |
| `BYTEA` / `BLOB` | `[]byte` | base64 | `encoding/json` já faz |
| `JSON` / `JSONB` | `json.RawMessage` | aninhado | não reserializar |
| `UUID` | `string` | string | |
| `NULL` | `nil` | `null` | |
| array PG | `[]any` | array | |

Isto **não é inferência**: é uma tabela escrita, revisável, com teste por
linha. É o mesmo princípio que fez o `typedTable` sobrepor só as duas colunas
do SDK e deixar o BigQuery tipar o resto.

No MySQL, `database/sql` devolve `[]byte` para quase tudo quando se lê em
`any`. `Rows.ColumnTypes()` dá o tipo declarado e é dele que a conversão sai —
sem isso, todo `DECIMAL` vira string de bytes e todo `INT` também.

**Incremental.** O `RunContext` já existe e já chega ao fetcher
(`v0.10.0`): `LogicalDate`, `Params`, `First`. O driver não inventa janela — ele
passa os `Args` que o fetcher escreveu. **Keyset, nunca `OFFSET`**: `OFFSET` é
O(n²) numa tabela grande, e o driver deve documentar isso em vez de oferecer um
`Offset` que convida ao erro.

### 3.2 `from.Files`

```go
from.Files{
    Path:   "s3://bucket/dia=2026-09-04/*.ndjson.gz",
    Format: sdk.FormatNDJSON,
}
```

**Um driver, três backends**, escolhidos pelo esquema do caminho: sem esquema
ou `file://` é disco, `s3://` é S3, `gs://` é GCS. É a decisão que também
destrava o Redshift (§3.5).

**Reuso real:** os decodificadores de NDJSON, CSV, JSON e XML já existem e já
recebem `io.Reader` (`extract.NewDecoder`). O driver de arquivo é listagem +
abertura + o decoder que já está testado.

- **Ordem determinística.** Listagem ordenada por chave. Duas execuções sobre o
  mesmo prefixo têm de produzir a mesma sequência, ou o `Preview` mente e o
  `ingestion_id` de um `Key` posicional muda.
- **Compressão por extensão.** `.gz` no MVP.
- **Sem bufferizar o objeto.** Streaming do S3/GCS direto para o decoder.
- **Arquivo vazio não é falha**, pela mesma razão que o `204` não é.

### 3.3 `to.Postgres` e `to.MySQL`

```go
to.Postgres{
    DSN:   "postgres://...",
    Table: "landing.pedidos",
}
```

**Caminho rápido.** Postgres: `COPY FROM STDIN`, via `pgx.CopyFrom`. MySQL não
tem equivalente confiável — `LOAD DATA LOCAL INFILE` costuma vir desabilitado
no servidor e no cliente —, então é `INSERT` multi-linha em lote, dentro de uma
transação, com tamanho de lote configurável.

**As colunas vêm da tabela, e é aqui que o SDK reusa o que já aprendeu.**
`CopyFrom` exige valores posicionais na ordem das colunas do destino. Ler o
schema de `information_schema.columns` e reconciliar contra os campos do
registro é **exatamente** o que o `reconcile` faz hoje para o `MERGE` do
BigQuery, com a mesma regra assimétrica:

- campo no registro que a tabela não tem → **erro nomeando o campo**
- coluna na tabela que o registro não traz → NULL, legítimo
- tipo incompatível → erro nomeando a coluna e os dois tipos

O `reconcile` sobe de `sdk/load` para `internal/core` e passa a servir os três
destinos SQL. Foi um defeito real e caro no BigQuery (`v0.12.0`); não repetir é
metade do valor desta fase.

**Dedup fica mais barato que no BigQuery.** O `DedupMerge` vira:

```sql
-- Postgres
INSERT INTO destino (...) SELECT ... FROM tmp
ON CONFLICT (ingestion_id) DO NOTHING

-- MySQL
INSERT IGNORE INTO destino (...) VALUES ...
```

Ambos exigem **índice único em `ingestion_id`**. O SDK confere que ele existe e
recusa nomeando-o se não existir. Não cria: um loader que sabe criar índice
sabe travar uma tabela de produção, e vale aqui o mesmo princípio de
`table.go:26`.

**As duas colunas de metadado**, no dialeto de cada um:

| | `ingestion_id` | `ingestion_loaded_at` |
|---|---|---|
| Postgres | `TEXT NOT NULL` | `TIMESTAMPTZ NOT NULL` |
| MySQL | `VARCHAR(36) NOT NULL` | `DATETIME(6) NOT NULL` |
| Redshift | `VARCHAR(36) NOT NULL` | `TIMESTAMPTZ NOT NULL` |

### 3.4 `to.Files`

```go
to.Files{
    Path:        "gs://bucket/landing/pedidos/",
    Format:      sdk.FormatNDJSON,
    PartitionBy: "ingestion_loaded_at",   // vira dt=2026-09-04/
    Compress:    true,
}
```

- **Atômico.** Local: escreve `.tmp` e renomeia. Objeto: um `PUT` só, que já é
  atômico. Ninguém pode ler meio arquivo.
- **NDJSON e CSV no MVP.** Parquet fica para depois: ele traz Arrow, que hoje só
  entra pela porta do BigQuery — para um consumidor que só usa arquivos seria
  peso novo, e a §1.2 existe para não fazer isso.
- **Sem `CreateTable`, sem `Dedup`.** Um diretório não tem schema nem chave
  única. Pedir qualquer um dos dois com este destino é erro dizendo isso, não
  uma flag que não faz nada.

### 3.5 `to.Redshift`

```go
to.Redshift{
    DSN:     "postgres://...:5439/dev",
    Table:   "landing.pedidos",
    Staging: "s3://bucket/stage/",
    IAMRole: "arn:aws:iam::123456789012:role/redshift-copy",
}
```

**`INSERT` linha a linha no Redshift é inviável** — é um banco colunar, e a
carga certa é `COPY` a partir do S3. O fluxo:

1. escreve o lote em `s3://.../stage/<run>/part-0001.ndjson` — **a mesma camada
   do `to.Files`**, e é por isso que Files vem antes no roadmap;
2. `COPY destino FROM 's3://...' IAM_ROLE '...' FORMAT AS JSON 'auto'`;
3. apaga o staged, a menos que `KeepStagedFile`.

**Dedup:** `COPY` para tabela de staging e `MERGE ... WHEN NOT MATCHED THEN
INSERT` (Redshift suporta desde 2023), com a lista de colunas **nomeada** —
pela razão que custou a `v0.12.0`: `INSERT ROW` casa por posição.

**Credencial:** `IAM_ROLE` como padrão. Chave de acesso na URL do `COPY` acaba
em log de query; se for suportada, tem de ser recusada em favor da role, ou no
mínimo redigida — o SDK já tem `redactURL`.

---

## 4. O que não generaliza, e a decisão honesta

**`CreateTable` sem inferência.** No BigQuery a `v0.16.0` resolve isso pedindo
ao próprio BigQuery que infira os tipos das colunas do cliente, e sobrepondo só
as duas do SDK. **Postgres, MySQL e Redshift não têm esse serviço.** Inferir
`NUMERIC(18,2)` a partir de um `float64` do `encoding/json` seria adivinhar — e
é a única coisa que este SDK decidiu não fazer.

Então, no MVP, para os três destinos SQL:

> A tabela precisa existir, ou você passa o DDL em `CreateSQL`.

O erro diz isso, e diz as colunas que o lote traz, para o DDL sair de uma
leitura. Um `CreateTable: true` sem `CreateSQL` num destino SQL é **erro
nomeando a limitação**, não uma inferência silenciosa.

**Redshift não roda local.** Não há imagem. As consequências, ditas em voz alta
em vez de escondidas:

- o dialeto SQL (`MERGE`, `COPY`) é testável só contra um cluster de verdade;
- o que dá para testar sem cluster é a **geração do SQL**, como funções puras —
  igual ao `mergeSQL`/`reconcile` de hoje, que existem justamente porque o SQL
  montado dentro de um método com cliente nunca tinha sido visto por um teste;
- o resto exige um `redshift-serverless` pequeno num ambiente de CI pago, e
  isso é uma decisão de custo que não é minha.

**Sem cluster, o driver Redshift é o único que sai com verificação parcial.**
Isso vai no README, não no rodapé.

---

## 5. Testes

`docker-compose.drivers.yml`, ligado por variável de ambiente como os testes de
integração do BigQuery já são:

| serviço | imagem | serve a |
|---|---|---|
| postgres | `postgres:17-alpine` | `from.Postgres`, `to.Postgres` |
| mysql | `mysql:8` | `from.MySQL`, `to.MySQL` |
| minio | `minio/minio` | `s3://` de `Files` e o staging do Redshift |

GCS não tem emulador bom; `gs://` continua indo contra o bucket real que a
suíte do BigQuery já usa (`zarv-development-94b6-bravis-it`).

**Por driver, no mínimo:**

1. um teste que prova que uma linha realmente entra ou sai — os em memória
   provam os bytes que montamos, não o que o servidor aceita, e foi a primeira
   execução dos de integração que achou quatro defeitos de uma vez;
2. o mapeamento de tipo, linha a linha da tabela do §3.1, incluindo `NULL`,
   `NUMERIC` e `JSONB`;
3. um teste que **falha** se o driver bufferizar em vez de fazer streaming;
4. o SQL gerado afirmado como função pura, sem cliente.

---

## 6. Roadmap

A ordem não é preferência: cada fase destrava a seguinte.

### Fase 0 — a costura · `v0.18.0`

`Reader` e `Writer`, os subpacotes `from` e `to`, e HTTP e BigQuery movidos
para trás das interfaces. **Nenhum driver novo.**

É a fase mais fácil de pular e a mais cara de pular. Feita depois de dois
drivers novos, ela reescreve os dois. E é ela que corta o binário do consumidor
de Postgres dos 21 MB de hoje.

*Pronto quando:* os dez exemplos e a suíte de integração do BigQuery passam sem
mudança de comportamento, só de forma; um consumidor que importa só
`sdk` + `from` + `to.Postgres` não tem `cloud.google.com` no `go list -deps`.

### Fase 1 — Files, os dois lados · `v0.19.0`

Local, S3 e GCS. Reusa os decodificadores; entrega a camada de staging que o
Redshift vai precisar.

*Pronto quando:* `./x/*.csv`, `s3://` e `gs://` leem e escrevem contra o MinIO e
contra o bucket real; ordem determinística provada com um teste que falharia
com listagem não ordenada.

### Fase 2 — Postgres, os dois lados · `v0.20.0`

O `reconcile` sobe para `internal/core`. `CopyFrom` na carga, `ON CONFLICT` na
dedup, a tabela de tipos com teste por linha.

*Pronto quando:* Postgres → Postgres roda ponta a ponta contra o container, com
dedup provada por carregar o mesmo lote duas vezes — o teste que já existe para
o BigQuery, portado.

### Fase 3 — MySQL, os dois lados · `v0.21.0`

Mais barato depois da fase 2: muda a lib, o dialeto e a estratégia de escrita;
a forma é a mesma.

*Pronto quando:* o mesmo pipeline da fase 2, com uma linha trocada, roda contra
o MySQL.

### Fase 4 — Redshift no load · `v0.22.0`

Só depende da fase 1 (staging em S3) e da 2 (reconcile). Sai com a limitação de
verificação do §4 escrita no README.

### Fase 5 — o que um lançamento exige · `v1.0.0-rc`

Não é feature, e é o que separa "compila" de "publicável":

- **matriz de compatibilidade** — qual driver suporta `Metadata`, `Dedup`,
  `CreateTable` e `Preview`, e o que acontece quando não suporta;
- **um exemplo executável por driver**, porque foi um exemplo que não rodava que
  achou o buraco do `03-basic-load`;
- **benchmark de carga** por destino, com número, para a documentação não
  prometer o que ninguém mediu;
- **teste de consumidor por driver** no módulo `examples`, que é a contramedida
  que este projeto já usa contra superfície inalcançável;
- **`CHANGELOG` com o diff de migração escrito por extenso** — é o que o
  consumidor copia.

### Sobre o "agressivo"

O que dá para paralelizar: as fases 2 e 3 são independentes depois da 0, e a 1
é independente das duas. O que **não** dá é começar por qualquer uma sem a
fase 0 — e essa é a única sequência rígida aqui.

O risco maior do roadmap não é técnico, é de escopo: nove drivers com
`Metadata`, `Dedup`, `CreateTable` e `Preview` são 36 combinações, e a §5 da
fase 5 existe porque prometer as 36 sem medir é como o `DeleteAfterLoad` chegou
à documentação com um default que ele não tinha.

---

## 7. O que não fazer

- **Não** adie a fase 0. Dois drivers escritos antes dela são dois drivers
  reescritos depois.
- **Não** infira tipo de coluna nos destinos SQL. Sem serviço de autodetect, é
  adivinhação — e ela voltaria justamente pelas colunas de dinheiro.
- **Não** crie índice, nem altere tabela, em nenhum destino. Divergência é erro,
  não migração. Vale o princípio de `table.go:26`.
- **Não** ofereça `Offset` no extract SQL. Convida ao O(n²) e a origem some do
  código de quem escreveu a query.
- **Não** deixe um driver aceitar opção que ele ignora. `Dedup` no `to.Files`,
  `ClusterBy` no `to.Postgres`, `RateLimiter` no `from.Files`: erro nomeando a
  opção e o driver. É a lição que a `v0.16.0` aplicou ao `AutoID`.
- **Não** monte SQL por concatenação sem crase/aspas em todo identificador.
  `full`, `range` e `comment` são reservadas, e o defeito já custou a `v0.12.0`.
- **Não** anuncie o Redshift como verificado enquanto não houver cluster.

---

## 8. A prova, fora do repositório

Pegue um fetcher de Postgres → Redshift escrito por alguém que nunca viu o SDK,
e responda lendo só o `main.go` dele:

> De onde vem, o que sai em cada coluna, para onde vai, e quanto pesa o binário?

As três primeiras a fase 0 já entrega. A quarta é a §1.2, e é a que diz se o
SDK está pronto para ser importado por quem não usa BigQuery.
