# SDK — o que cada driver suporta

**Vale para** `sdk/v0.24.0` · **Atualizado em** 2026-09-04

O que funciona com o quê, e o que acontece quando não funciona. Um driver que
**ignora** uma opção não aparece aqui: neste SDK ele a recusa nomeando a opção
e o driver.

---

## 1. Origens

| | `from.HTTP` | `from.Files` |
|---|---|---|
| formatos | JSON, NDJSON, CSV, XML | JSON, NDJSON, CSV, XML |
| streaming | sim | sim |
| `Preview` | sim | sim |
| `Stats` (páginas, tentativas, bytes) | sim | páginas = arquivos; sem tentativas |
| paginação | Link header, cursor, offset | não se aplica |
| retry com backoff | sim (429, 5xx, rede) | não |
| `RateLimiter` | sim | não |
| `Records` | sim | não se aplica: um arquivo não é uma resposta |
| compressão | o que o servidor negociar | `.gz` pela extensão |
| ordem determinística | ordem da paginação | **sim, garantida** |

### `from.HTTP`, ponto a ponto

| | |
|---|---|
| `Method`, `Body`, `Header` | POST e PUT com corpo e cabeçalhos próprios |
| `Timeout` / `TotalTimeout` | por tentativa / pela caminhada inteira |
| `RetryConfig` | tentativas, backoff exponencial, jitter, `Retry-After` |
| `FollowLinks` | RFC 8288, `rel="next"` |
| `CursorKey` | cursor no corpo, devolvido como parâmetro de mesmo nome |
| `PageKey` + `FirstPage` | número de página, avança de um em um |
| `OffsetKey` + `PageSize` | offset em linhas, avançado a cada página |
| `MaxPages` | teto da caminhada; cursor repetido também para |
| `DataKey` | desembrulha o array; **recusado junto de `Records`** |
| `Header["Cookie"]` | semeia o jar; `Set-Cookie` renova por nome na página seguinte |
| `Auth.Value` + `Apply` | de onde vem o segredo e como ele entra na requisição |
| `Auth.TTL` | cacheia o login em memória, sob trava; nunca toca disco |
| `Auth.Refresh` | um GET antes da primeira página; o jar absorve o `Set-Cookie` |
| `Auth.Refresh.ExpiresAt` + `WarnAfter` | avisa antes da credencial vencer — no log **e** em `Stats.CredentialExpiry` |

**Duas estratégias de paginação juntas é erro**, não regra de precedência — a
perdedora seria um campo escrito que não faz nada.

**Todo 2xx** chega ao `Records`, `204` e `206` incluídos. Não-2xx é erro com
status e corpo, com retry onde faz sentido.

### `from.Files`, ponto a ponto

| | |
|---|---|
| `Path` | `./x/*.csv`, `/var/dados/`, `s3://b/p/*.ndjson`, `gs://b/p/` |
| `Store` | `nil` é disco; `s3.New(...)`, `gcs.New(...)` |
| `NoHeader` | CSV sem cabeçalho, chaveado por `field_N` |

Diretório vazio é resultado, não falha. Um `.gz` que não é gzip falha nomeando
o arquivo.

---

## 2. Destinos

| | `bigquery.Table` | `to.Files` |
|---|---|---|
| `Columns` (declaração) | sim | sim |
| as duas colunas de ingestão | do `Transform`; `NOT NULL` quando declaradas | do `Transform` |
| `Dedup: DedupMerge` | sim, via `MERGE` | **recusado** |
| criar o destino | `CreateTable`, `CreateSQL` | cria o diretório |
| particionamento | dia em `ingestion_loaded_at` | `PartitionBy` vira `campo=valor/` |
| clusterização | `ClusterBy` | não se aplica |
| compressão | do formato staged | `Compress` (gzip) |
| escrita atômica | job do BigQuery | temp+rename, ou um PUT |
| formatos escritos | NDJSON | NDJSON, CSV |

### As combinações recusadas, e por quê

| combinação | o que acontece |
|---|---|
| `DedupMerge` sem `ingestion_id` em `Columns` | **erro** — o merge casa nessa coluna |
| `DedupMerge` + `RequirePartitionFilter` | **erro** — o merge varre todas as partições e não dá para escopar |
| opções de partição sem `ingestion_loaded_at` em `Columns` | **erro** — particiona-se nessa coluna |
| `Dedup` em `to.Files` | **erro** — um diretório não tem chave para casar |
| Parquet em `to.Files` | **erro** — traria o Arrow para quem só queria um arquivo |
| `Records` + `DataKey` | **erro** — os dois dizem onde estão os registros |
| `Path` de nuvem sem `Store`, ou `Store` de outro esquema | **erro** nomeando os dois lados |
| `CreateTable` sem `CreateSQL` num destino SQL | **erro** — o SDK não infere tipo (fases 2–4) |

### `bigquery.Table`, ponto a ponto

| | |
|---|---|
| `Project`, `Dataset` | caem no ambiente; ver as constantes `Env*` |
| `Name` | sem padrão |
| `StagingBucket`, `StagingPrefix` | acima de `InlineLimit`, passa pelo GCS |
| `InlineLimit` | zero usa 5000 |
| `CreateTable` | tri-estado: `nil` deixa a engine decidir |
| `CreateSQL` | a sua DDL, conferida contra a tabela produzida |
| `ClusterBy` | colunas conferidas contra as linhas antes de submeter |
| `PartitionExpiration` | zero mantém para sempre |
| `KeepStagedFile` | o zero value apaga |

O SDK **nunca** altera uma tabela que existe. Divergência é erro, não migração.

---

## 3. O que está provado, e como

Toda linha acima tem teste. O que se prova contra o serviço de verdade, e não
só em memória:

| driver | contra o quê | testes |
|---|---|---|
| `from.HTTP` | `httptest` — HTTP de verdade | 39 |
| `from.Files` (disco) | sistema de arquivos | 10 |
| `to.Files` (disco) | sistema de arquivos | 12 |
| `from.Files` / `to.Files` (S3) | MinIO, em container | 4 |
| `from.Files` / `to.Files` (GCS) | bucket real | 1 |
| `bigquery.Table` | BigQuery real | 17 |
| `bigquery.Table` | em memória | 64 |

São **236 testes** no módulo, e 80% de cobertura com a integração ligada.

```bash
# os de disco e de HTTP rodam sempre
go test ./...

# os de nuvem pedem as variáveis
docker compose -f docker-compose.drivers.yml up -d minio
BREVIS_IT_S3_ENDPOINT=http://localhost:9000 \
BREVIS_IT_PROJECT=meu-projeto BREVIS_IT_DATASET=bravis_it \
BREVIS_IT_BUCKET=meu-bucket \
  go test ./... -run Integration
```

**Sem as variáveis eles pulam**, e a suíte normal segue offline. É de propósito:
um teste que precisa de credencial e falha sem ela vira um teste que todo mundo
aprende a ignorar.

---

## 4. O que ainda não é verdade

Dito aqui para não ser descoberto em produção:

- **Postgres, MySQL e Redshift não existem ainda.** Fases 2 a 4 do
  [plano](plan/2026-09-04-sdk-drivers-mvp.md).
- **Parquet não é escrito** por nenhum destino.
- **`from.Files` não é incremental**: ele lê o que o caminho nomeia. Uma janela
  se faz no próprio caminho (`dia=2026-09-04/`), com o `RunContext`.
- **O SDK infere os tipos das colunas do cliente** ao criar uma tabela no
  BigQuery — delegando ao autodetect do próprio BigQuery. As duas colunas dele
  são declaradas; as suas, não. Ver §13 de [`SDK_DECISOES.md`](SDK_DECISOES.md).
- **`to.Files` não deduplica**, e portanto uma reexecução escreve o lote de
  novo, num arquivo novo. Quem resolve isso é a camada de baixo.
