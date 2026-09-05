#!/usr/bin/env bash
# Prova que um consumidor só compila o que importa.
set -euo pipefail
ARVORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../sdk" && pwd)"
MODULO="github.com/AreteAcademy/brevis/sdk"

verificar() {
  local nome="$1" imports="$2" corpo="$3" proibidos="$4"
  local dir; dir="$(mktemp -d)"; trap 'rm -rf "$dir"' RETURN
  cd "$dir"
  cat > go.mod <<EOF
module poda/$nome

go 1.23

require $MODULO v0.0.0

replace $MODULO => $ARVORE
EOF
  { echo "package main"; echo; echo "import ("; echo "$imports"; echo ")"; echo;
    echo "func main() { $corpo }"; } > main.go
  go mod tidy >/dev/null 2>&1
  local deps; deps="$(go list -deps ./...)"
  local total; total="$(echo "$deps" | wc -l | tr -d ' ')"
  local falhou=0
  for p in $proibidos; do
    local n; n="$(echo "$deps" | grep -c "$p" || true)"
    if [ "$n" != "0" ]; then
      echo "❌ $nome compila $n pacote(s) de $p, e não deveria"
      falhou=1
    fi
  done
  [ "$falhou" = "0" ] && echo "✅ $nome: $total pacotes, nenhum driver alheio"
  return $falhou
}

verificar "arquivos" \
  "	\"$MODULO/from\"
	\"$MODULO/to\"" \
  "_ = from.Files{}; _ = to.Files{}" \
  "jackc/pgx cloud.google.com aws-sdk-go"

verificar "postgres" \
  "	\"$MODULO/from/postgres\"" \
  "_ = postgres.Query{}" \
  "cloud.google.com aws-sdk-go"

verificar "mysql" \
  "	\"$MODULO/from/mysql\"" \
  "_ = mysql.Query{}" \
  "jackc/pgx cloud.google.com aws-sdk-go"

verificar "bigquery" \
  "	\"$MODULO/to/bigquery\"" \
  "_ = bigquery.Table{}" \
  "jackc/pgx aws-sdk-go"
