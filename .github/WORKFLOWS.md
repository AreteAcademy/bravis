# GitHub Actions Workflows

Automação de CI/CD para o projeto Bravis.

## 🔄 Workflows Disponíveis

### 1. **test.yml** — Test & Lint
Executa em: `push` (main, master, develop) e `pull_request`

**Fases:**
- ✅ **Test**: Testes com coverage, integração com Codecov
- ✅ **Lint**: gofmt, go vet, go mod tidy, golangci-lint
- ✅ **Security**: Gosec (segurança)
- ✅ **Build**: Compila SDK e exemplos

**Badges & Reports:**
- Coverage report via Codecov
- Security scan via GitHub Security tab

**Falha em:**
- Testes não passam
- gofmt problemas
- golangci-lint issues
- go mod desatualizado

---

### 2. **publish-sdk.yml** — Publish SDK
Executa em: `push` com tag `sdk/v*`

**Fases:**
- ✅ **Validate**: Valida semver (v0.1.0, v1.2.3-beta, etc)
- ✅ **Test**: Roda testes antes de publicar
- ✅ **Release**: Cria GitHub Release
- ✅ **Notify**: Notifica Go proxy (pkg.go.dev)
- ✅ **Verify**: Aguarda indexação

**Resultado:**
```
✅ GitHub Release criada
📍 Disponível em: https://pkg.go.dev/github.com/AreteAcademy/bravis/sdk@v0.1.0
🔗 Go proxy atualizado
```

**Uso:**
```bash
git tag sdk/v0.1.0
git push origin sdk/v0.1.0
```

---

### 3. **build-site.yml** — Build & Deploy Site
Executa em: `push` (main) e `pull_request` quando `site/` muda

**Fases:**
- ✅ **Build**: Valida HTML, CSS, links
- ✅ **Lighthouse**: Performance CI (em PRs)
- ✅ **Docker**: Build e push para Docker Hub (em main)

**Resultado:**
- 🐳 Docker image: `username/bravis-site:latest`
- 📊 Lighthouse scores
- 🔍 Link validation

**Configuração Necessária:**
```
Secrets:
- DOCKER_USERNAME
- DOCKER_PASSWORD
```

---

### 4. **release-notes.yml** — Generate Release Notes
Executa em: `release` (SDK releases)

**Automático:**
- Extrai commits desde última versão
- Gera notas com features, links, docs
- Atualiza GitHub Release

**Exemplo de Output:**
```markdown
# 🚀 SDK v0.1.0

## Installation
go get github.com/AreteAcademy/bravis/sdk@v0.1.0

## Recent Changes
- feat(extract): add retry with backoff
- fix(load): handle empty responses
- docs: update README

## Features
- 📤 Extract with retry
- 📥 Load to BigQuery
- 🔐 Idempotency
```

---

### 5. **quality.yml** — Code Quality
Executa em: `push` (main) e `pull_request`

**Fases:**
- ✅ **Format**: gofmt check
- ✅ **Vet**: go vet check
- ✅ **Coverage**: Calcula e reporta
- ✅ **Badge**: Atualiza coverage badge

**Resultado:**
- 📊 Coverage report no PR
- 🏷️ Badge atualizado em main

---

### 6. **dependabot.yml** — Dependency Updates
Executa: Semanalmente (segundas, 03:00 UTC)

**Verifica:**
- Go dependencies (sdk/)
- GitHub Actions

**Cria:**
- PRs automáticas com updates
- Labels: `dependencies`, `go`, `ci`

**Política:**
- Max 5 PRs abertas
- Auto-review por maintainers

---

## 📊 Status Badges

Adicione ao README.md:

```markdown
[![Tests](https://github.com/AreteAcademy/bravis/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/AreteAcademy/bravis/actions/workflows/test.yml)
[![SDK Version](https://img.shields.io/github/v/tag/AreteAcademy/bravis?filter=sdk/*&label=SDK)](https://github.com/AreteAcademy/bravis/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/AreteAcademy/bravis/sdk)](https://goreportcard.com/report/github.com/AreteAcademy/bravis/sdk)
[![codecov](https://codecov.io/gh/AreteAcademy/bravis/branch/main/graph/badge.svg)](https://codecov.io/gh/AreteAcademy/bravis)
```

---

## 🔑 Secrets Necessários

Configure em: `Settings → Secrets and variables → Actions`

### Docker Hub (opcional, para `build-site.yml`)
```
DOCKER_USERNAME  = seu-usuario
DOCKER_PASSWORD  = seu-token (não a senha)
```

### Codecov (opcional, para `test.yml`)
- Automático (repositório público)
- Ou configure: `CODECOV_TOKEN`

---

## 🚀 Triggers & Eventos

| Workflow | On Push | On PR | On Release | On Tag |
|----------|---------|-------|-----------|--------|
| test.yml | ✅ | ✅ | — | — |
| publish-sdk.yml | — | — | — | ✅ (sdk/v*) |
| build-site.yml | ✅ (site/) | ✅ (site/) | — | — |
| release-notes.yml | — | — | ✅ | — |
| quality.yml | ✅ | ✅ | — | — |
| dependabot.yml | — | — | — | — (semanal) |

---

## 📝 Workflow Tips

### Testar workflows localmente
```bash
# Instalar act
brew install act

# Rodar teste localmente
act push -l
```

### Verificar status
```bash
# CLI GitHub
gh run list --repo AreteAcademy/bravis

# Ver detalhes de uma run
gh run view <RUN_ID> --repo AreteAcademy/bravis
```

### Rerun um workflow
```bash
# Re-executar
gh run rerun <RUN_ID> --repo AreteAcademy/bravis
```

---

## 🔍 Monitoramento

### GitHub Actions Dashboard
→ `Actions` tab no repositório

### Per-workflow status
→ Each workflow tem badge próprio

### Codecov
→ https://codecov.io/gh/AreteAcademy/bravis

### pkg.go.dev
→ https://pkg.go.dev/github.com/AreteAcademy/bravis/sdk

---

## 🛠️ Mantendo os Workflows

### Atualizar Go version
Edite em: `.github/workflows/*.yml`
```yaml
go-version: '1.26'  # atualize aqui
```

### Atualizar actions
Dependabot cuida disso automaticamente

### Adicionar novo workflow
1. Crie arquivo em `.github/workflows/`
2. Commit e push
3. Workflow aparece no Actions tab

---

## ❌ Troubleshooting

**Testes falhando em CI mas passando localmente?**
- Verificar go version
- Executar `go mod tidy`
- Verificar environment variables

**Docker push falhando?**
- Verificar DOCKER_USERNAME e DOCKER_PASSWORD
- Confirmar token tem permissão push

**pkg.go.dev não atualiza?**
- Aguarde 5-10 minutos após tag
- Verificar tag format: `sdk/v0.1.0`
- Forçar: `curl https://proxy.golang.org/github.com/AreteAcademy/bravis/sdk/@v/v0.1.0.info`

---

**Dúvidas?** Abra uma issue ou veja a [documentação oficial do GitHub Actions](https://docs.github.com/en/actions)