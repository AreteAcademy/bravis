# SDK — como acrescentar um driver

**Vale para** `sdk/v0.21.0` · **Atualizado em** 2026-09-04

O roteiro das fases 2 a 4 de
[`plan/2026-09-04-sdk-drivers-mvp.md`](plan/2026-09-04-sdk-drivers-mvp.md). A
fase 1 (Files) já saiu, na `v0.20.0`, e serve de modelo: leia `sdk/from/files.go`
e `sdk/to/files.go` ao lado deste documento.
Para o mapa, veja [`SDK_ARQUITETURA.md`](SDK_ARQUITETURA.md); para as decisões
que este roteiro pressupõe, [`SDK_DECISOES.md`](SDK_DECISOES.md).

---

## 1. O esqueleto

Um driver é um tipo com os campos que **só ele** tem, mais um método:

```go
// sdk/from/postgres.go
package from

type Postgres struct {
    DSN       string
    Query     string
    Args      []any
    FetchSize int
}

func (p Postgres) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error)
func (p Postgres) Describe() string
```

```go
// sdk/to/postgres.go
package to

type Postgres struct {
    DSN       string
    Table     string
    BatchSize int
}

func (p Postgres) Write(ctx context.Context, records []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error)
func (p Postgres) Describe() string
```

O mesmo nome nos dois pacotes é de propósito: `from.Postgres` tem `Query`,
`to.Postgres` tem `Table`, e nenhum carrega o campo do outro.

---

## 2. As nove regras

### 2.1 Um driver com dependência mora no próprio pacote

`from` e `to` guardam os drivers que só precisam da biblioteca padrão. Qualquer
um com SDK de fornecedor atrás — BigQuery, Postgres, Redshift — vai para o seu
próprio pacote: `to/bigquery`, `to/postgres`.

Dividir pacote com um driver caro tem o mesmo efeito que a raiz importá-lo.
Aconteceu na `v0.20.0`: `to.BigQuery` e `to.Files` juntos faziam escrever um
arquivo compilar o Google, 461 pacotes onde deviam ser 195.

### 2.2 A raiz não pode importar o seu pacote

Se `sdk` passar a importar `from/postgres`, todo consumidor compila o `pgx` — e
a propriedade que a fase 0 comprou morre. `examples/consumer/pruning_test.go`
acusa. **Não conserte o teste; conserte o import.**

Acrescente o seu caso lá, com o controle: quem importa o seu pacote *tem* de
receber a sua dependência, senão o teste passaria com um driver que não carrega
nada.

### 2.3 O backend de nuvem também é um valor

Se `from.Files` importasse S3 e GCS, ler um CSV local compilaria os dois. Por
isso `core.Store` é passado em vez de escolhido dentro do driver, e mora em
`store/s3` e `store/gcs`.

Vale para qualquer driver que fale com mais de um backend: **o que varia vira
valor, e o valor mora no seu próprio pacote.**

### 2.4 Streaming, sempre

`Read` devolve um `iter.Seq2` que **produz sob demanda**. Um driver que
materializa a origem inteira antes de devolver põe um export de 5 GB na
memória.

Escreva o teste que falha se você bufferizar — o do HTTP é
`extract.TestBodyStreamsFully`, e ele existe porque essa regressão já aconteceu:
um `cancelAttempt()` cedo demais truncava o corpo, e nenhum teste via.

### 2.5 Nada de inferir tipo

O SDK não adivinha o tipo de uma coluna. No BigQuery a `v0.16.0` resolve isso
delegando a inferência ao próprio BigQuery — carrega numa tabela descartável com
autodetect, lê o schema e sobrepõe só as duas colunas que são do SDK.

**Postgres, MySQL e Redshift não têm esse serviço.** Então, para eles:

> A tabela precisa existir, ou você passa o DDL em `CreateSQL`.

Um `CreateTable: true` sem `CreateSQL` num destino SQL é **erro nomeando a
limitação**, e a mensagem lista as colunas que o lote traz, para o DDL sair de
uma leitura. Não é uma lacuna a preencher depois com inferência: é a decisão.

### 2.6 Opção que o driver não suporta é erro, não silêncio

`Dedup` num destino de arquivos, `ClusterBy` no Postgres, `RateLimiter` numa
origem de disco: erro nomeando a opção e o driver.

É a lição que a `v0.16.0` aplicou ao `AutoID` — proveniência junto dele seria
escrita e nunca lida, então é recusada nomeando os campos. Um campo aceito e
ignorado é o defeito que este SDK mais achou em si mesmo.

### 2.7 SQL gerado é função pura

Monte o SQL fora do método que precisa de conexão:

```go
func mergeSQL(dest, temp string, cols []string, key string) string
func reconcile(dest, incoming Schema) (cols []string, err error)
```

Não é estilo. O `MERGE` do BigQuery ficou **três versões** com `INSERT ROW`, que
casa colunas por **posição**, com um comentário afirmando que casava por nome —
e nenhum teste unitário jamais tinha visto a string gerada, porque ela nascia
dentro de um método com cliente.

E **crase ou aspas em todo identificador**: `full`, `range` e `comment` são
reservadas e aparecem em coluna de consumidor de verdade.

### 2.8 A reconciliação é assimétrica

Ao casar o registro com o destino, use a mesma regra que o `reconcile` já usa:

| situação | o que fazer |
|---|---|
| campo no registro que o destino não tem | **erro** nomeando o campo |
| coluna no destino que o registro não traz | segue, fica NULL |
| tipos incompatíveis no mesmo nome | **erro** nomeando a coluna e os dois tipos |

Descartar dado em silêncio é o pior modo de falhar: some sem sinal. Coluna que
fica NULL é legítima numa landing.

Na fase 2 o `reconcile` sobe de `sdk/load` para `internal/core` e passa a servir
os três destinos SQL.

### 2.9 Não altere nem apague nada

Vale para todos os drivers o princípio escrito no godoc de `load.prepareTable`: um loader que sabe
fazer `ALTER` sabe apagar história. Divergência é erro, não migração. E não crie
índice — confira que o índice único de `ingestion_id` existe e recuse nomeando-o
se não existir.

---

## 3. As duas colunas de metadado, por dialeto

| | `ingestion_id` | `ingestion_loaded_at` |
|---|---|---|
| BigQuery | `STRING NOT NULL` | `TIMESTAMP NOT NULL` |
| Postgres | `TEXT NOT NULL` | `TIMESTAMPTZ NOT NULL` |
| MySQL | `VARCHAR(36) NOT NULL` | `DATETIME(6) NOT NULL` |
| Redshift | `VARCHAR(36) NOT NULL` | `TIMESTAMPTZ NOT NULL` |

`WriteOptions.Metadata` diz se acrescentar; `WriteOptions.AutoID` diz se o id é
aleatório. A proveniência já vem resolvida no `Envelope` — o driver não lê o
registro do cliente para descobrir o que identifica uma linha.

## 4. Dedup, por dialeto

| destino | como |
|---|---|
| BigQuery | `MERGE ... WHEN NOT MATCHED THEN INSERT (cols) VALUES (...)` |
| Postgres | staging + `INSERT ... ON CONFLICT (ingestion_id) DO NOTHING` |
| MySQL | `INSERT IGNORE`, com índice único em `ingestion_id` |
| Redshift | `COPY` para staging + `MERGE`, com as colunas **nomeadas** |
| Files | não suportado — erro dizendo isso |

Todos exigem `Metadata`, porque casam em `ingestion_id`. Todos são recusados
junto de `AutoID`, porque um id aleatório não casa com nada.

---

## 5. Mapeamento de tipo para os drivers SQL

O registro vira JSON, então cada tipo precisa de uma escolha **escrita**:

| SQL | Go | JSON | por quê |
|---|---|---|---|
| `NUMERIC` / `DECIMAL` | `string` | string | `float64` perde precisão em dinheiro |
| `TIMESTAMPTZ` | `time.Time` | RFC 3339 | |
| `DATE` | `time.Time` | `YYYY-MM-DD` | sem hora falsa |
| `BYTEA` / `BLOB` | `[]byte` | base64 | `encoding/json` já faz |
| `JSON` / `JSONB` | `json.RawMessage` | aninhado | não reserializar |
| `UUID` | `string` | string | |
| `NULL` | `nil` | `null` | |
| array PG | `[]any` | array | |

Uma tabela escrita e testada linha a linha não é inferência: é uma decisão
revisável. No MySQL, `database/sql` devolve `[]byte` para quase tudo quando se
lê em `any` — a conversão sai de `Rows.ColumnTypes()`, e sem isso todo `INT`
vira string de bytes.

---

## 6. Testes

Ligue os containers com `docker-compose.drivers.yml` e trave por variável de
ambiente, como os testes do BigQuery já são:

| serviço | serve a | variável |
|---|---|---|
| `postgres:17-alpine` | `from.Postgres`, `to.Postgres` | `BRAVIS_IT_PG_DSN` |
| `mysql:8` | `from.MySQL`, `to.MySQL` | `BRAVIS_IT_MYSQL_DSN` |
| `minio/minio` | `s3://` de `Files` e o staging do Redshift | `BRAVIS_IT_S3_ENDPOINT` |

O compose já existe: `docker-compose.drivers.yml`, na raiz. O MinIO já é usado
pelos testes do `Files`.

GCS não tem emulador bom; `gs://` vai contra o bucket real que a suíte do
BigQuery já usa.

**Por driver, no mínimo:**

1. um teste que prova que uma linha **realmente** entra ou sai. Os em memória
   provam os bytes que montamos, não o que o servidor aceita — e foi a primeira
   execução dos de integração que achou quatro defeitos de uma vez;
2. o mapeamento de tipo, linha a linha da tabela do §5, com `NULL`, `NUMERIC` e
   `JSONB`;
3. um que **falha** se o driver bufferizar em vez de fazer streaming;
4. o SQL gerado afirmado como função pura, sem cliente;
5. o caso de poda em `examples/consumer/pruning_test.go`, com o controle;
6. um exemplo executável em `examples/`, que roda de primeira. Foi um exemplo
   que não rodava que achou o buraco do `03-basic-load`.

**Verifique que o teste morde.** Reverta a correção e confirme que ele falha,
antes de dar por bom. Esta é a regra que mais achou defeito neste projeto.

---

## 7. Checklist de pronto

- [ ] `Read`/`Write` e `Describe` implementados
- [ ] driver com dependência está no próprio pacote
- [ ] a raiz continua sem importar o pacote — teste de poda com o controle,
      **incluindo o pipeline completo dos dois lados**
- [ ] backend que varia mora no próprio pacote, passado como valor
- [ ] streaming provado por um teste que falharia sem ele
- [ ] tipos mapeados por tabela escrita, com teste por linha
- [ ] `CreateTable` sem inferência: tabela existente ou `CreateSQL`
- [ ] opção não suportada é erro nomeando a opção e o driver
- [ ] SQL gerado é puro e testado; identificadores citados
- [ ] `reconcile` assimétrico
- [ ] nada de `ALTER`, `DROP` ou criação de índice
- [ ] integração contra container, travada por variável de ambiente
- [ ] exemplo executável que roda de primeira
- [ ] `CHANGELOG` com o diff de migração por extenso
- [ ] `go test ./... -race` verde, `golangci-lint run ./...` limpo
- [ ] `cmd/bravis-sdk` continua compilando (módulo próprio, pin sobe depois da tag)

---

## 8. A pergunta final

Pegue um fetcher escrito por quem nunca viu o SDK e responda lendo só o
`main.go`:

> De onde vem, o que sai em cada coluna, para onde vai, e quanto pesa o binário?

As três primeiras a arquitetura já entrega. A quarta é a poda, e é ela que diz
se o SDK está pronto para quem não usa BigQuery.
