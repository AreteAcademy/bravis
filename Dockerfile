# syntax=docker/dockerfile:1.7
#
# Binario estatico em imagem distroless. Diferente da imagem de tasks do Leoflow,
# que precisava de Python e bash para o agente, aqui o processo E o binario — nao
# ha shell a executar, entao distroless e possivel e desejavel.

# BUILDPLATFORM: compila SEMPRE na arquitetura nativa do builder e cruza para a
# de destino. Sem isso, o build arm64 num runner amd64 roda sob emulacao QEMU e
# leva minutos em vez de segundos.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# O CSS e embutido no binario (web/assets/embed.go). Ele vai versionado no repo
# justamente para que a imagem nao precise do Tailwind; se sumir, o //go:embed
# falha aqui, no build, e nao em producao com a pagina sem estilo.
RUN test -f web/assets/app.css || { echo "web/assets/app.css ausente — rode 'make generate'"; exit 1; }
# As fontes e os bundles UMD sao servidos do binario. Sem eles a UI carrega, mas
# com a tipografia do sistema e a tela da DAG em branco — falhas silenciosas que
# so aparecem no navegador. Falhar aqui, com mensagem, e melhor.
RUN test -f web/assets/fonts/inter-latin.woff2 || { echo "web/assets/fonts ausente"; exit 1; }
RUN test -f web/assets/vendor/xyflow.js || { echo "web/assets/vendor ausente"; exit 1; }

# Versao carimbada no binario. `bravis version` dentro do container e a unica
# forma confiavel de saber o que esta rodando quando a tag da imagem foi movida.
ARG VERSAO=dev
ARG COMMIT=""
ARG DATA=""

# TARGETOS/TARGETARCH vem do buildx. Sem eles, um build multi-arch compilaria
# tudo para a arquitetura do builder e a imagem arm64 traria um binario amd64 —
# que so falha no primeiro `docker run` do cluster.
ARG TARGETOS
ARG TARGETARCH

# -trimpath e -s -w tiram caminhos absolutos e tabelas de debug.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X main.Versao=${VERSAO} -X main.Commit=${COMMIT} -X main.Data=${DATA}" \
      -o /out/bravis ./cmd/bravis

# Duas imagens do MESMO binario, porque os dois papeis tem exigencias opostas.
#
# `api` so serve HTTP: nao executa nada, entao distroless (sem shell, superficie
# minima) e possivel e desejavel.
FROM gcr.io/distroless/static-debian12:nonroot AS api
ARG VERSAO=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Bravis" \
      org.opencontainers.image.description="Engine de orquestracao e transformacao de dados" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/bravis" \
      org.opencontainers.image.version="${VERSAO}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/bravis /usr/local/bin/bravis
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["bravis"]
CMD ["serve"]

# `worker` roda os passos `run:` dos workflows — e isso EXIGE um shell. Rodar o
# scheduler na imagem distroless deixaria todo run falhando com "no such file or
# directory", que e o pior tipo de erro: correto e incompreensivel.
FROM alpine:3.20 AS worker
ARG VERSAO=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Bravis worker" \
      org.opencontainers.image.description="Bravis com shell, para executar os passos dos workflows" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/bravis" \
      org.opencontainers.image.version="${VERSAO}" \
      org.opencontainers.image.revision="${COMMIT}"
RUN apk add --no-cache ca-certificates tini
COPY --from=build /out/bravis /usr/local/bin/bravis
RUN adduser -D -u 65532 bravis
USER bravis
ENTRYPOINT ["/sbin/tini", "--", "bravis"]
CMD ["scheduler"]
