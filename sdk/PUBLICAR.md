# Publicação do SDK

Guia para publicar o Bravis SDK em pkg.go.dev.

## Pré-requisitos

1. ✅ Repositório público em `github.com/AreteAcademy/bravis`
2. ✅ Módulo aninhado em `sdk/` com `go.mod` próprio
3. ✅ Path do módulo definido: `github.com/AreteAcademy/bravis/sdk`
4. ✅ Testes passando
5. ✅ `go vet` limpo

## Checklist de Publicação

### 1. Preparar o SDK

```bash
cd sdk/
go mod tidy
go test ./...
go vet ./...
```

Verificar que não há erros.

### 2. Commit e Tag

```bash
git add sdk/
git commit -m "feat(sdk): initial release with extract and load

Extract abstracts HTTP data collection with retry, timeout, format handling.
Load writes to BigQuery with automatic strategy selection.

- extract: CSV, NDJSON, JSON with retry on 429 and 5xx
- load: inline and GCS staging strategies
- ingestion_id: deterministic UUID v5 for idempotency
- Stdlib-first design: minimal dependencies"

# Tag com prefixo do diretório (obrigatório para módulos aninhados)
git tag sdk/v0.1.0
git push origin main
git push origin sdk/v0.1.0
```

### 3. Publicação em pkg.go.dev

A publicação é automática quando:
1. Tag `sdk/v0.1.0` é feita no repositório público
2. Go proxy sincroniza (geralmente < 5 min)
3. pkg.go.dev indexa (< 5 min depois)

Para forçar a indexação:

```bash
GOPROXY=proxy.golang.org go list -m github.com/AreteAcademy/bravis/sdk@v0.1.0
```

### 4. Verificar Publicação

- Acesse: https://pkg.go.dev/github.com/AreteAcademy/bravis/sdk
- Verificar que a documentação está correta
- Testar importação em outro projeto:

```bash
go get github.com/AreteAcademy/bravis/sdk@v0.1.0
```

## Versionamento Semântico

| Versão | Mudança |
|--------|---------|
| **0.1.0** | Primeira release; extract + load básico |
| **0.2.0** | Adicionar XML, paginação, Storage Write API |
| **1.0.0** | API estável, sem mudanças quebradoras |

Mude apenas:
- `MINOR` para novas features
- `PATCH` para bugfixes
- Marque mudanças quebradoras como `BREAKING CHANGE:` no commit

## Consumindo o SDK

Uma vez publicado, o SDK pode ser importado diretamente:

```bash
go get github.com/AreteAcademy/bravis/sdk@v0.1.0
```

Sem precisar de `replace` local.

## Atualizar Módulo Raiz

Se o módulo raiz (Bravis) precisar do SDK, adicione ao `go.mod`:

```go
require github.com/AreteAcademy/bravis/sdk v0.1.0
```

Mas isso é **opcional** — o SDK é independente.

## Troubleshooting

### Tag não encontrada no Go proxy

Verificar:
1. Tag foi feita com o prefixo correto: `sdk/v0.1.0` (não `v0.1.0`)
2. Tag foi feita no commit correto (após `git add sdk/`)
3. Push foi executado: `git push origin sdk/v0.1.0`

Se já foi feito:
```bash
git tag -d sdk/v0.1.0
git push origin :sdk/v0.1.0
# Corrigir e refazer
```

### Documentação não aparece no pkg.go.dev

Verificar:
1. `doc.go` está no pacote raiz (`sdk/doc.go`)
2. Documentação segue formato correto (comentário em inglês)
3. Aguardar 5-10 min para indexação

Forçar reindexação:
```bash
curl https://pkg.go.dev/github.com/AreteAcademy/bravis/sdk@v0.1.0?tab=doc
```

### Dependência não resolve no proxy

Às vezes o proxy precisa de tempo extra. Tente:
```bash
go clean -modcache
GOPROXY=direct go get github.com/AreteAcademy/bravis/sdk@v0.1.0
```

## Referências

- [Go Modules](https://go.dev/doc/modules/version-numbers)
- [pkg.go.dev](https://pkg.go.dev/about)
- [Go Module Mirror](https://proxy.golang.org/)
- [Nested Modules](https://github.com/golang/go/wiki/Modules#is-it-possible-to-add-a-module-to-a-multi-module-repository)