.DEFAULT_GOAL := help
BIN := bin/bravis

# --- Imagem -----------------------------------------------------------------
# REGISTRY/NAMESPACE ficam em variavel para que um fork publique no proprio
# espaco sem editar arquivo nenhum: `make image-push NAMESPACE=outro`.
REGISTRY  ?= docker.io
NAMESPACE ?= daniel3843
IMAGEM    ?= $(REGISTRY)/$(NAMESPACE)/bravis
VERSAO    ?= $(shell cat VERSION)
# `-dirty` quando ha mudanca nao commitada. Sem o sufixo, `bravis version`
# dentro da imagem apontaria para um commit que NAO contem o codigo publicado —
# e e por essa informacao que se rastreia um incidente.
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo desconhecido)$(shell git diff --quiet HEAD 2>/dev/null || echo -dirty)
DATA      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# As duas arquiteturas que importam: Apple Silicon no desenvolvimento e amd64/arm64
# no cluster. Uma imagem so-amd64 roda no Mac por emulacao, devagar e escondendo
# problemas de arquitetura ate o deploy.
PLATAFORMAS ?= linux/amd64,linux/arm64
DB_URL := postgres://bravis:bravis@localhost:5432/bravis?sslmode=disable
# Banco SEPARADO para os testes de integracao. Desde que a stack local passou a
# subir um scheduler de verdade, rodar os testes contra `bravis` era uma corrida:
# o scheduler do compose reivindicava os itens que o teste acabara de enfileirar
# e o criterio de aceite falhava sem nada estar errado.
TEST_DB_URL := postgres://bravis:bravis@localhost:5432/bravis_test?sslmode=disable

help: ## Lista os alvos
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: generate ## Compila o binario em bin/ (gera templ e css antes)
	@go build -trimpath -o $(BIN) ./cmd/bravis

test: ## Roda os testes (os de integracao pulam sem Postgres)
	@go test ./...

test-int: test-db ## Roda tudo, inclusive integracao (exige `make up`)
	@BRAVIS_TEST_DATABASE_URL="$(TEST_DB_URL)" go test ./... -count=1

test-db: ## Cria e migra o banco de testes (idempotente)
	@docker compose exec -T postgres psql -U bravis -d postgres -tAc \
	  "SELECT 1 FROM pg_database WHERE datname='bravis_test'" | grep -q 1 \
	  || docker compose exec -T postgres createdb -U bravis bravis_test
	@BRAVIS_DATABASE_URL="$(TEST_DB_URL)" go run ./cmd/bravis migrate up >/dev/null

check: ## gofmt + vet + testes (portao antes de commitar)
	@test -z "$$(gofmt -l cmd internal migrations)" || { echo "gofmt pendente:"; gofmt -l cmd internal migrations; exit 1; }
	@go vet ./...
	@go test ./...

dev: ## Hot reload: recompila e reinicia a cada mudanca (exige `make up` antes)
	@command -v air >/dev/null || { echo "instale: go install github.com/air-verse/air@latest"; exit 1; }
	@test -x bin/tailwindcss || $(MAKE) tailwind-install
	@BRAVIS_DATABASE_URL="$(DB_URL)" air

tailwind-install: ## Baixa o binario standalone do Tailwind (sem Node)
	@mkdir -p bin
	@ARCH=$$(uname -m | sed 's/x86_64/x64/'); OS=$$(uname -s | tr 'A-Z' 'a-z'); \
	 curl -sSL -o bin/tailwindcss \
	   "https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-$$OS-$$ARCH" \
	 && chmod +x bin/tailwindcss && echo "bin/tailwindcss instalado"

generate: ## Gera os _templ.go e o CSS
	@command -v templ >/dev/null || { echo "instale: go install github.com/a-h/templ/cmd/templ@latest"; exit 1; }
	@templ generate
	@./bin/tailwindcss -i web/assets/app.src.css -o web/assets/app.css --minify

image: generate ## Constroi as imagens para a arquitetura local (nao publica)
	@docker build --target api  -t $(IMAGEM):$(VERSAO)        -t $(IMAGEM):latest \
	  --build-arg VERSAO=$(VERSAO) --build-arg COMMIT=$(COMMIT) --build-arg DATA=$(DATA) .
	@docker build --target worker -t $(IMAGEM):$(VERSAO)-worker -t $(IMAGEM):latest-worker \
	  --build-arg VERSAO=$(VERSAO) --build-arg COMMIT=$(COMMIT) --build-arg DATA=$(DATA) .
	@echo "  $(IMAGEM):$(VERSAO)  e  $(IMAGEM):$(VERSAO)-worker"

image-push: generate ## Publica multi-arch no registry (exige `docker login`)
	@docker buildx inspect bravis >/dev/null 2>&1 || docker buildx create --name bravis --use
	@docker buildx build --builder bravis --platform $(PLATAFORMAS) --target api \
	  --build-arg VERSAO=$(VERSAO) --build-arg COMMIT=$(COMMIT) --build-arg DATA=$(DATA) \
	  -t $(IMAGEM):$(VERSAO) -t $(IMAGEM):latest --push .
	@docker buildx build --builder bravis --platform $(PLATAFORMAS) --target worker \
	  --build-arg VERSAO=$(VERSAO) --build-arg COMMIT=$(COMMIT) --build-arg DATA=$(DATA) \
	  -t $(IMAGEM):$(VERSAO)-worker -t $(IMAGEM):latest-worker --push .
	@echo "publicado: $(IMAGEM):$(VERSAO) (+ -worker)"

image-smoke: ## Confere que as imagens locais sobem e reportam a versao
	@docker run --rm $(IMAGEM):$(VERSAO) version
	@docker run --rm $(IMAGEM):$(VERSAO)-worker version
	@# --entrypoint: o worker entra por `tini -- bravis`, entao um `sh` solto
	@# viraria subcomando do bravis. O shell existe para o WORKFLOW usar.
	@docker run --rm --entrypoint sh $(IMAGEM):$(VERSAO)-worker -c 'echo "  shell ok no worker"'

up: ## Sobe Postgres + API + scheduler localmente
	@docker compose up --build -d
	@echo "api em http://localhost:8080/health"

down: ## Derruba o ambiente local
	@docker compose down

logs: ## Acompanha os logs da API
	@docker compose logs -f api

smoke: ## Verifica /health e /ready contra o ambiente local
	@printf 'health: '; curl -fsS localhost:8080/health && echo
	@printf 'ready:  '; curl -fsS localhost:8080/ready  && echo

.PHONY: help build test test-int test-db check up down logs smoke dev generate tailwind-install image image-push image-smoke
