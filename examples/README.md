# Bravis SDK Examples

Exemplos práticos do SDK em ação.

## 📋 Exemplos Disponíveis

### 1. Basic Extract
**Arquivo:** `01_basic_extract.go`

O exemplo mais simples: extrair dados CSV de uma URL.

```bash
go run examples/01_basic_extract.go -url "https://example.gov/api/data.csv"
```

**O que você aprende:**
- Usar `extract.CSV()` com configuração mínima
- Iterar sobre resultados com `iter.Seq2`
- Tratamento de erros básico

### 2. Advanced Extract
**Arquivo:** `02_advanced_extract.go`

Configuração completa de extract com retry, timeout e guard function.

```bash
go run examples/02_advanced_extract.go
```

**O que você aprende:**
- Configurar retry com backoff exponencial
- Usar timeouts per-attempt e total
- Guard function para validar responses
- Headers customizados (User-Agent, Authorization)
- Logging estruturado

### 3. Basic Load
**Arquivo:** `03_basic_load.go`

Escrever dados em BigQuery (requer autenticação Google Cloud).

```bash
# Setup: autenticar com Google Cloud
gcloud auth application-default login

# Rodar exemplo
export GOOGLE_CLOUD_PROJECT=my-project
go run examples/03_basic_load.go -project $GOOGLE_CLOUD_PROJECT
```

**O que você aprende:**
- Criar um `Loader` com configuração
- Preparar `Envelope` com payload JSON
- Load automático (inline ou GCS)
- Interpretar resultados

### 4. Complete Pipeline
**Arquivo:** `04_complete_pipeline.go`

Exemplo realista: Extract → Transform → Load.

```bash
# Requer autenticação Google Cloud
gcloud auth application-default login

go run examples/04_complete_pipeline.go \
  -url "https://api.example.gov/data.csv" \
  -project my-project \
  -provider gov_agency \
  -entity permits \
  -dataset landing

# Ou com dry-run (só extrai, não carrega)
go run examples/04_complete_pipeline.go \
  -url "https://api.example.gov/data.csv" \
  --dry-run
```

**O que você aprende:**
- Pipelineend-to-end realista
- Tratamento de erros em escala
- Transformação de dados
- Logging estruturado com `slog`
- Versioning e retry inteligente

## 🚀 Pré-requisitos

### Para Extract (exemplos 1, 2, 4)
- Go 1.25+
- Conexão com Internet
- (Nenhuma autenticação necessária)

### Para Load (exemplos 3, 4)
- Google Cloud SDK instalado
- Credenciais configuradas:
  ```bash
  gcloud auth application-default login
  ```
- Projeto GCP com BigQuery habilitado
- Permissões: `bigquery.datasets.get`, `bigquery.tables.create`, `bigquery.tables.update`

## 📊 Dados de Teste

Os exemplos usam URLs públicas (quando disponíveis) ou dados em memória.

Para testar com dados reais:

```bash
# CSV público
go run examples/01_basic_extract.go -url "https://raw.githubusercontent.com/mledoze/countries/master/countries.json"

# Ou use seu próprio servidor
python3 -m http.server 8000 --directory ./data
go run examples/01_basic_extract.go -url "http://localhost:8000/sample.csv"
```

## 🔧 Configuração Avançada

### Retry Configuration
```go
RetryConfig: &sdk.RetryConfig{
    MaxAttempts:    5,              // quantas vezes tentar
    InitialBackoff: 500 * time.Millisecond,
    MaxBackoff:     30 * time.Second,
    JitterFraction: 0.2,            // 20% jitter para evitar thundering herd
}
```

### Load Configuration
```go
&sdk.LoadConfig{
    ProjectID:       "my-project",
    Dataset:         "landing",
    StagingBucket:   "my-bucket",
    ThresholdForGCS: 5000,          // acima disso, usa GCS
    Format:          "ndjson",      // ou "csv", "parquet"
    DeleteAfterLoad: true,          // limpar file após load
}
```

### Guard Function
```go
Guard: func(status int, body []byte) error {
    // Validar ANTES de decodificar
    if !json.Valid(body) {
        return fmt.Errorf("invalid JSON")
    }
    return nil
}
```

## 📈 Performance

| Exemplo | Rows | Time | Memory |
|---------|------|------|--------|
| Extract 1M CSV | 1,000,000 | ~30s | ~50MB |
| Load 5K inline | 5,000 | ~2s | ~100MB |
| Load 50K GCS | 50,000 | ~10s | ~200MB |

## 🐛 Troubleshooting

### "Permission denied" ao carregar
- Verificar credenciais: `gcloud auth application-default print-access-token`
- Verificar projeto: `gcloud config get-value project`
- Verificar permissões no IAM

### "Table not found"
- BigQuery leva ~30s para criar tabela
- Verificar dataset existe: `bq ls my-dataset`
- Verificar nome (case-sensitive)

### "Context deadline exceeded"
- Aumentar `TotalTimeout` em `sdk.Fonte`
- Para load grande, aumentar timeout do contexto

### "API rate limit"
- Reduza `concurrency` em load
- Use rate limiter em extract:
  ```go
  RateLimiter: rate.NewLimiter(rate.Limit(100), 1), // 100 req/s
  ```

## 📚 Links Úteis

- [SDK Documentation](../sdk/README.md)
- [Bravis GitHub](https://github.com/AreteAcademy/bravis)
- [BigQuery Go Client](https://cloud.google.com/go/docs/reference/cloud.google.com/go/bigquery)

## 💡 Próximos Passos

1. **Adapte um exemplo** para sua fonte de dados
2. **Configure retry** para sua API
3. **Teste com dry-run** antes de carregar
4. **Monitore logs** para otimizar performance
5. **Configure alertas** no BigQuery para anormalidades

## 📝 Exemplo Real: API de Governo

```bash
# Simular API de governo (dados públicos)
go run examples/04_complete_pipeline.go \
  -url "https://www.dados.gov.br/api/dados" \
  -project meu-projeto \
  -provider dados_gov_br \
  -entity public_datasets \
  --dry-run
```

---

**Dúvidas?** Abra uma issue em https://github.com/AreteAcademy/bravis/issues