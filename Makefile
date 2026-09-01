.DEFAULT_GOAL := help
BIN := bin/bravis
DB_URL := postgres://bravis:bravis@localhost:5432/bravis?sslmode=disable

help: ## Lista os alvos
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## Compila o binario em bin/
	@go build -trimpath -o $(BIN) ./cmd/bravis

test: ## Roda os testes (os de integracao pulam sem Postgres)
	@go test ./...

test-int: ## Roda tudo, inclusive integracao (exige `make up`)
	@BRAVIS_TEST_DATABASE_URL="$(DB_URL)" go test ./... -count=1

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
	@templ generate
	@./bin/tailwindcss -i web/assets/app.src.css -o web/assets/app.css --minify

up: ## Sobe Postgres + API localmente
	@docker compose up --build -d
	@echo "api em http://localhost:8080/health"

down: ## Derruba o ambiente local
	@docker compose down

logs: ## Acompanha os logs da API
	@docker compose logs -f api

smoke: ## Verifica /health e /ready contra o ambiente local
	@printf 'health: '; curl -fsS localhost:8080/health && echo
	@printf 'ready:  '; curl -fsS localhost:8080/ready  && echo

.PHONY: help build test test-int check up down logs smoke dev generate tailwind-install
