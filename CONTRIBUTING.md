# Contribuindo com o Bravis

## Estrutura

Três módulos Go independentes, cada um com seu `go.mod`:

| diretório | módulo | o que é |
|---|---|---|
| `/` | `github.com/AreteAcademy/bravis` | a engine |
| `/sdk` | `github.com/AreteAcademy/bravis/sdk` | SDK público, publicado no proxy |
| `/cmd/bravis` | `github.com/AreteAcademy/bravis/cmd/bravis` | CLI |
| `/examples` | `github.com/AreteAcademy/bravis/examples` | exemplos, com `replace` para `../sdk` |

O SDK tem módulo próprio para manter as dependências mínimas: hoje são três
diretas (`bigquery`, `storage`, `uuid`). **Pense duas vezes antes de somar uma
quarta** — o argumento do projeto é o tamanho do binário.

## Rodando os testes

```bash
cd sdk && go test ./... -race
cd examples && go build ./... && go test ./...
```

Os testes do SDK são offline: `httptest.Server` no lugar de APIs reais. Nenhum
teste da suíte normal toca a rede ou o GCP.

Os testes de integração do `load` são a exceção e ficam travados atrás de
`-short` e de variáveis de ambiente:

```bash
export BRAVIS_IT_PROJECT=meu-projeto
export BRAVIS_IT_DATASET=bravis_it     # precisa existir
export BRAVIS_IT_BUCKET=meu-bucket     # para a estratégia GCS
go test ./load/... -run Integration
```

## Lint

O CI roda `golangci-lint` v2.13.2. Para rodar igual localmente:

```bash
cd sdk && golangci-lint run --timeout=5m ./...
```

Não suprima achado com `//nolint` sem explicar o porquê na mesma linha.

## Padrões que o projeto leva a sério

**Campo público que não faz nada é pior que campo ausente.** Quem preenche
acredita ter configurado algo. Se não dá para implementar agora, não declare —
ou recuse explicitamente, como `LoadConfig.Format` faz com `"parquet"`.

**Verificação que não pode falhar não é verificação.** O projeto já teve um
passo de CI com `go build ... || true` seguido de `echo "✅ compilou"`, que
escondeu seis exemplos que não compilavam. Se um passo não puder ficar
vermelho, ele não está testando nada.

**Documentação que descreve comportamento inexistente é pior que documentação
ausente.** Os exemplos em `examples/` compilam no CI e os `Example` de godoc
são compilados pelo `go test` justamente para isso.

**Número errado em telemetria é pior que número ausente**, porque ninguém
desconfia dele.

## Commits

Convencional, e o corpo explica **por quê**, não o quê — o diff já mostra o
quê:

```
fix(sdk/load): batch load jobs, honest formats, and the envelope contract

table.Inserter() é a streaming insert API: cobrada por linha, e as linhas
ficam num buffer onde o DML não as enxerga por até 90 minutos...
```

Escopos: `sdk`, `sdk/extract`, `sdk/load`, `cli`, `ci`, `site`, `docs`.

## Publicando o SDK

```bash
git tag sdk/v0.2.2      # o prefixo do diretório é obrigatório
git push origin sdk/v0.2.2
```

O `publish-sdk.yml` cuida do resto. Duas coisas que custaram uma versão
queimada:

1. **O proxy é imutável.** Depois que uma versão é buscada uma vez, o conteúdo
   está congelado — apagar a tag não desfaz nada. Por isso existe um gate que
   compila um consumidor descartável antes do release.
2. **A URL do proxy usa case-encoding**: `AreteAcademy` vira `!arete!academy`.
   Use `go list -m`, que codifica sozinho.

A `v0.1.0` está publicada e quebrada para sempre. Não repita.

## Abrindo um PR

- Um assunto por PR.
- Teste que falharia sem a mudança. Para correção de bug, o teste é a prova de
  que o bug existia.
- CI verde. Não peça revisão com vermelho.
