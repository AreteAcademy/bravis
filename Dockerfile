# syntax=docker/dockerfile:1.7
#
# Binario estatico em imagem distroless. Diferente da imagem de tasks do Leoflow,
# que precisava de Python e bash para o agente, aqui o processo E o binario — nao
# ha shell a executar, entao distroless e possivel e desejavel.

FROM golang:1.25-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# -trimpath e -s -w tiram caminhos absolutos e tabelas de debug.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bravis ./cmd/bravis

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bravis /usr/local/bin/bravis
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["bravis"]
CMD ["serve"]
