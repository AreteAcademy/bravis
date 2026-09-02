# SDK Bravis — Próximos Passos

O SDK foi criado com sucesso com o path de módulo correto: `github.com/AreteAcademy/bravis/sdk`

## ✅ Concluído

- [x] Estrutura do SDK criada (`extract`, `load`, tipos compartilhados)
- [x] 8 testes de extract implementados (retry, timeout, guard, paginação, etc)
- [x] Load com estratégias automáticas (inline e GCS)
- [x] Ingestion ID com UUID v5 determinístico
- [x] Path de módulo definido: `github.com/AreteAcademy/bravis/sdk`
- [x] Documentação completa
- [x] Guia de publicação (`sdk/PUBLICAR.md`)

## 🚀 Publicação em pkg.go.dev

### 1. Configurar repositório remoto

```bash
cd /Users/zarv/Workspace/Zarv/bravis

# Verificar remote atual
git remote -v

# Adicionar se não existir
git remote add origin https://github.com/AreteAcademy/bravis.git
# Ou atualizar se estiver errado
git remote set-url origin https://github.com/AreteAcademy/bravis.git
```

### 2. Push do código

```bash
# Fazer commit se houver mudanças
git add .
git commit -m "feat: add bravis SDK with extract and load

- extract: HTTP data collection with retry, timeout, format handling
- load: BigQuery writer with automatic strategy selection
- ingestion_id: deterministic UUID v5 for idempotency
- Stdlib-first: minimal dependencies (only cloud.google.com/go)"

# Push para main
git push origin main
```

### 3. Criar e publicar tag

```bash
# Criar tag do SDK (com prefixo do diretório)
git tag sdk/v0.1.0

# Push da tag
git push origin sdk/v0.1.0

# Verificar publicação no Go proxy
GOPROXY=proxy.golang.org go list -m github.com/AreteAcademy/bravis/sdk@v0.1.0
```

### 4. Verificar em pkg.go.dev

Aguardar 5-10 minutos e acessar:
```
https://pkg.go.dev/github.com/AreteAcademy/bravis/sdk
```

## 📋 Checklist de Validação

Antes de publicar, executar:

```bash
cd sdk

# Compilar
go build ./...

# Testes
go test ./...

# Linting
go vet ./...
go fmt ./...

# Dependências
go mod tidy

# Verificar tamanho
ls -lh go.sum

# Documentação
grep -r "Package" *.go | head -5
```

## 📚 Exemplos de Uso

### Extract
```go
import "github.com/AreteAcademy/bravis/sdk/extract"

lines, _ := extract.CSV(ctx, extract.Fonte{
    URL: "https://example.gov/api/data.csv",
})

for env, err := range lines {
    // Process env
}
```

### Load
```go
import (
    "github.com/AreteAcademy/bravis/sdk"
    "github.com/AreteAcademy/bravis/sdk/load"
)

loader, _ := load.New(ctx, &sdk.LoadConfig{
    ProjectID: "my-project",
    Dataset:   "landing",
})

result, _ := loader.Load(ctx, "example", "transactions", envelopes...)
```

## 🔗 Links Úteis

- GitHub: https://github.com/AreteAcademy/bravis
- SDK Package: https://pkg.go.dev/github.com/AreteAcademy/bravis/sdk
- Go Modules: https://go.dev/doc/modules
- pkg.go.dev: https://pkg.go.dev/about

## 📝 Versionamento

- **v0.1.0** — Release inicial (extract + load básico)
- **v0.2.0** — XML, paginação, Parquet
- **v1.0.0** — API estável

Ver `sdk/PUBLICAR.md` para detalhes de publicação futura.

## ❓ Dúvidas

Se houver problemas:

1. Verificar `go mod tidy` resolve dependências
2. Confirmar tag está em: `git tag -l sdk/*`
3. Confirmar push: `git ls-remote --tags origin`
4. Aguardar cache (5-10 min)
5. Forçar reindexação: `GOPROXY=direct go get github.com/AreteAcademy/bravis/sdk@sdk/v0.1.0`

## 🎯 Após Publicação

1. Atualizar exemplos no README principal
2. Criar issue no repositório: "SDK published to pkg.go.dev"
3. Documentar em `docs/SDK.md` como importar
4. Considerar criar exemplo no `examples/` usando o SDK