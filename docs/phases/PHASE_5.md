# PHASE 5 — UI FOUNDATION

Concluída em 2026-09-01. Inclui hot reload de desenvolvimento, pedido fora do plano.

## Implemented

| item | onde |
|---|---|
| templ | `web/layouts`, `web/pages`, `web/components` |
| Tailwind (standalone, sem Node) | `web/assets/app.src.css` → `app.css` |
| Layout + Sidebar | `web/layouts/base.templ` |
| Dashboard, Projetos, Workflows, Execuções | `web/pages/pages.templ` |
| Handlers SSR | `internal/api/ui.go` |
| Camada de leitura | `internal/infrastructure/postgres/leitura.go` |
| **Hot reload** | `.air.toml`, `make dev` |

Verificado servindo dados reais: dashboard com 26 runs em sucesso, tabela de
execuções com workflow, origem e slot, workflows com cron `0 * * * *` e um
marcado como "nunca rodou".

## Architecture Decisions

**SSR puro, sem SPA.** A §17 pede páginas server-rendered com ilhas interativas
apenas onde a interação justifica. Nenhum JavaScript nesta fase — o HTML chega
pronto. React Flow entra na PHASE 6, e só no editor de DAG.

**Tailwind standalone, sem Node.** O binário oficial (~80 MB, baixado por
`make tailwind-install`) elimina `npm`, `package.json` e `node_modules` de um
projeto que é Go de ponta a ponta. O `app.css` gerado vai versionado, então a
aplicação roda sem a toolchain.

**`@source` explícito no CSS.** O Tailwind v4 detecta arquivos sozinho, mas os
`_templ.go` gerados ficam fora das heurísticas padrão — e uma classe não detectada
some do CSS sem aviso.

**Camada de leitura separada dos repositórios de escrita.** A UI precisa de
projeções achatadas e agregados que não correspondem às entidades de domínio.
Misturar faria o domínio carregar campos que só existem para a tela.

**Duração só quando início e fim existem.** Calcular com um dos lados nulo
produziria número sem significado; a tabela mostra `—`.

**Estado vazio comunica ausência, não erro.** Uma tabela vazia sem explicação faz
o operador procurar defeito onde não há; cada uma sugere o comando que a preenche
(`brevis publish`, `brevis scheduler`).

**`GET /{$}` para a raiz.** Sem o `{$}`, o `ServeMux` do Go trata `/` como
prefixo e captura toda rota não registrada — inclusive erros de digitação, que
passariam a renderizar o dashboard em vez de 404.

**A UI é opcional no servidor.** `NewServer(..., ui)` aceita `nil`: um processo
que só serve health check não precisa das páginas, e exigi-las acoplaria o
servidor ao banco sem necessidade.

## Hot reload

`make dev`. A cadeia é ordenada de propósito:

```
templ generate  →  tailwindcss  →  go build  →  restart
```

`templ generate` **antes** do build: invertido, o binário sairia com os
`_templ.go` antigos e a mudança no `.templ` não apareceria.

`exclude_regex` ignora `_templ.go`: eles são gerados pelo próprio `pre_cmd`, e sem
excluí-los cada geração dispararia outro rebuild — laço infinito.

Verificado ponta a ponta: editar `web/layouts/base.templ` mudou a página servida
em ~10s, sem intervenção.

## Tests

`go test ./...` passa (11 pacotes). As páginas não têm teste automatizado —
registrado nas limitações.

Validação manual:

```
GET /            200  11.470 bytes   metricas com dados reais
GET /runs        200  16.283 bytes   26 runs, origem e slot
GET /workflows   200   3.491 bytes   cron e "nunca rodou"
GET /projects    200   1.960 bytes
GET /assets/app.css  200  16.372 bytes
```

## Known Limitations

- **Sem teste automatizado das páginas.** Um teste de handler renderizando com
  repositório falso caberia; ficou de fora por tempo.
- **Sem live updates.** A §17 pede "Server Rendered + Live Updates" para runs; hoje
  é preciso recarregar. SSE ou polling entra junto com o streaming de logs.
- **Sem paginação.** `/runs` mostra as 100 mais recentes, fixo.
- **Sem detalhe de run.** Não há página por execução, nem visualização de
  `task_runs` — que, aliás, seguem sem ser populados desde a PHASE 2.
- **Sem autenticação.** Qualquer um com acesso à porta vê tudo.
- **templUI não foi usado.** A §17 o lista; os componentes aqui são templ puro com
  Tailwind. Adotá-lo depois é possível, e não muda a arquitetura.
- O `docker-compose.yml` sobe um container `api` com imagem própria; ele conflita
  na porta 8080 com o `make dev`. Rodar `docker compose stop api` antes.

## Next Phase

**PHASE 6 — REACT FLOW**: visualização e edição de DAG como ilha interativa,
dentro das páginas SSR que esta fase estabeleceu.

---

## Correção pós-fase — assets em 404 (2026-09-01)

As páginas respondiam 200, mas `/assets/app.css` dava 404 no container: a UI
aparecia **sem estilo nenhum**, o que parece defeito da página e não do caminho.

Causa: o handler servia do sistema de arquivos, com caminho relativo —

```go
http.FileServer(http.Dir("web/assets"))
```

— enquanto o `Dockerfile` é distroless e copia **apenas o binário**. Não existe
`web/assets` no container. O mesmo quebrava ao rodar o `brevis` de qualquer
diretório que não a raiz do repositório.

Corrigido embutindo o CSS com `//go:embed`, como as migrations já eram. Verificado
nos dois caminhos que falhavam: binário rodando de `/tmp` e container distroless
reconstruído — `/assets/app.css` responde 200 com `content-type: text/css` nos
dois.

O `Dockerfile` ganhou uma checagem: se `web/assets/app.css` sumir do repositório,
o build falha ali, com mensagem, em vez de gerar uma imagem que serve páginas sem
estilo. E `make build` passou a depender de `generate`.

**Lição de método**: testei a UI apenas com o binário rodando da raiz do repo, onde
o caminho relativo funciona por acidente. O container — que eu mesmo escrevi como
distroless — nunca foi exercitado com a UI antes de eu declarar a fase concluída.
