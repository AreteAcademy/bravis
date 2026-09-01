.DEFAULT_GOAL := help
BIN := bin/bravis

help: ## Lista os alvos
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## Compila o binario em bin/
	@go build -trimpath -o $(BIN) ./cmd/bravis

test: ## Roda os testes (os de integracao pulam sem Postgres)
	@go test ./...

test-int: ## Roda tudo, inclusive integracao (exige `make up`)
	@BRAVIS_TEST_DATABASE_URL='postgres://bravis:bravis@localhost:5432/bravis?sslmode=disable' go test ./... -count=1

check: ## gofmt + vet + testes (portao antes de commitar)
	@test -z "$$(gofmt -l cmd internal migrations)" || { echo "gofmt pendente:"; gofmt -l cmd internal migrations; exit 1; }
	@go vet ./...
	@go test ./...

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

.PHONY: help build test test-int check up down logs smoke
