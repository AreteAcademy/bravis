# PHASE 6 — REACT FLOW

Concluída em 2026-08-31. Cobre os cinco itens de visualização da §20 —
DAG Visualization, Custom Nodes, Execution States, Live Updates e Node Inspector.
O **DAG Editor fica de fora por decisão**: o plano diz "a visualização vem antes
da edição", e editar um grafo sem antes conseguir lê-lo é construir na ordem
errada.

## Implemented

| item | onde |
|---|---|
| JSON no formato React Flow | `internal/api/graph.go` |
| Layout calculado no servidor | reusa `graph.Niveis` |
| Página do workflow (definição publicada) | `GET /workflows/{slug}` |
| Página da execução (snapshot + estado) | `GET /runs/{id}` |
| Ilha React sem bundler | `web/assets/dag.js` |
| Shim do `react/jsx-runtime` | `web/assets/jsx-shim.js` |
| React + ReactDOM + React Flow (UMD, embutidos) | `web/assets/vendor/` |
| Slot de assets por página | `layouts.BaseCom` + `layouts.Ilha` |
| `task_runs` finalmente populado | `postgres/taskruns.go` + `Runner.Persist` |
| Exit code preservado até o banco | `app.ErroDePasso` |

## Architecture Decisions

**O layout sai do servidor, não do navegador.** A §20 é explícita: React Flow é
camada de visualização, nunca fonte da verdade. As posições vêm do mesmo
`graph.Niveis()` que o executor usa para decidir o que roda em paralelo — então
dois nós lado a lado na tela são os que rodam juntos **de fato**. Um layout
automático no cliente desenharia um palpite bonito e possivelmente mentiroso.

**A página da execução desenha o snapshot do Run, não a definição atual.** Se o
workflow foi editado depois, a tela de uma execução passada continua mostrando o
grafo que rodou (§22). São dois endpoints por isso, e não um com parâmetro
opcional.

**UMD vendorizado, sem npm e sem CDN.** ~350 KB embutidos no binário via
`//go:embed`. A §15 proíbe Node no build; um CDN traria dependência de rede
externa em runtime, e a UI precisa funcionar num cluster sem saída para a
internet. O custo é atualizar à mão — aceitável para três arquivos.

**Shim próprio para `react/jsx-runtime`.** O bundle UMD do React Flow depende
dele, e o React 18 não publica esse módulo em UMD — só ESM/CJS. Sem o shim, a
tela fica em branco com `jsxRuntime is not defined`. A reimplementação é fiel:
`jsx`/`jsxs` diferem do `createElement` apenas por receberem os filhos dentro de
props e a key como terceiro argumento.

**`React.createElement` puro, sem JSX.** Verboso, mas transpilar exigiria a
toolchain Node que a §15 tira do caminho. O custo fica confinado a um arquivo.

**Live update por polling, não WebSocket.** O estado muda em segundos, não em
milissegundos. Um GET a cada 2s não precisa de conexão persistente nem de
reconexão, e **para sozinho** quando a run atinge estado terminal — por isso o
JSON carrega `terminal`. Sem esse campo, cada run concluída deixaria um poll
eterno.

`failed` não conta como terminal: a máquina de estados da §7 permite
`failed → retrying`, e congelar a tela ali esconderia um retry em andamento. O
cliente resolve pelo intervalo — 2s enquanto ativo, 10s em falha, nada quando
terminal.

**Assets por página, não no layout.** Só a tela da DAG carrega React. Colocar os
scripts no `Base` faria cada página SSR pagar por uma interatividade que não usa.

**Arrastar e reconectar desligados.** É visualização; permitir mover nós daria a
impressão de que a posição significa algo editável, quando ela é derivada do
grafo. Pan e zoom seguem livres.

**Grafo cíclico responde 422, não 500.** Um ciclo gravado no banco é dado
inválido, não falha do servidor — e a mensagem precisa dizer isso na tela.

## Dívidas fechadas nesta fase

**`task_runs` era escrito por ninguém desde a PHASE 2.** A tabela existia no
schema, o retry era por Run inteiro e não havia estado por passo. "Execution
States" depende exatamente disso, então o `Runner` ganhou `Persist`/`RunID`
opcionais — opcionais porque `brevis run` local não tem banco, e exigir um
tornaria a execução ad-hoc dependente de infraestrutura.

**O exit code se perdia dentro da mensagem de erro.** `tentar()` colapsava o
evento de falha num `fmt.Errorf`, e `task_runs.exit_code` ficava sempre nulo.
Agora é `*ErroDePasso` tipado; distinguir 127 (comando não encontrado) de 2 (erro
da aplicação) deixou de exigir leitura de log.

## Tests

`go test ./...` passa. Cinco testes novos em `internal/api/graph_test.go`, sobre
um diamante — o menor grafo em que layout errado aparece:

- níveis viram colunas, e os dois nós paralelos saem no **mesmo x** e em y distintos;
- workflow sem execução: todo nó `pending`, sem `run_id`;
- run: estado, duração, erro e exit code por nó; nó que não rodou continua cinza;
  aresta animada só a que chega no nó em execução;
- run terminal marcada como tal (o que faz o polling parar);
- 404 / 400 / 422 para workflow inexistente, uuid malformado e ciclo.

Mais um em `runner_test.go`: exit code 3 sobrevive de evento → erro tipado.

A ordem de carga dos assets foi exercitada fora do browser (`vm` do Node, com
DOM mínimo): os cinco arquivos carregam na ordem da página, `ReactFlow` expõe
`ReactFlow/Background/Controls/Handle/Position`, e o shim preserva key, children
e props.

## Validação ponta a ponta

DAG em diamante com um passo que falha de propósito, publicada e executada pelo
scheduler local:

```
run 09eae9ee status running terminal false
 extrair            x=0    y=0    success  1026ms
 transformar_ok     x=260  y=-55  running
 transformar_falha  x=260  y=55   failed   24ms  step "transformar_falha": saiu com codigo 2
 publicar           x=520  y=0    pending
arestas: extrair->transformar_ok*, extrair->transformar_falha,
         transformar_ok->publicar, transformar_falha->publicar   (* = animada)
```

Os dois nós paralelos no mesmo x, o nó não executado em cinza, e o container
reconstruído servindo `/runs/{id}` com os cinco scripts na ordem correta.

## Known Limitations

- **Sem editor de DAG.** Deliberado (ver topo).
- **Polling, não push.** Duas requisições por segundo por aba aberta numa run
  ativa. Vira SSE quando o streaming de logs entrar.
- **Sem logs por passo na tela.** O inspetor mostra estado, duração, tentativa,
  exit code e erro — não a saída do processo. Falta persistir stdout/stderr.
- **Run órfã fica "running" para sempre.** `Queue.Recuperar` existe e ninguém a
  chama num laço; matar o scheduler no meio deixa a run pendurada, e agora isso
  aparece na tela. É dívida da PHASE 2 que esta fase tornou visível.
- **Sem teste de renderização da ilha.** A validação do JS é sintaxe + carga em
  `vm`; não há headless browser no CI.
- **Nós grandes não são agrupados.** Uma DAG de 200 passos desenha 200 cards; o
  plano prevê colapsar subgrafos, não implementado.
- **Sem autenticação** — segue valendo desde a PHASE 5.

## Next Phase

**PHASE 7 — DBT ENGINE** (§6): o compilador de SQL com `ref()`/`source()`,
materializações e o grafo derivado dos modelos. É o que substitui o dbt, e a
visualização desta fase serve o resultado dele sem mudança.


---

## Redesign da UI — identidade Aretê (2026-08-31)

Pedido depois da fase: aproximar a lista de workflows do **Airflow**, o Overview
do **Kestra**, e vestir tudo com a identidade da
[Aretê Academy](https://areteacademy.com.br) — "filosófica e clean".

### Paleta e tipografia vêm do site, não de aproximações

Os tokens em `web/assets/app.src.css` carregam os **mesmos valores** do
`style.css` da Aretê: pergaminho `#f4efe4`, superfície `#fffdf8`, tinta
`#21180f`, texto secundário `#6e6254`, ouro `#aa8450` / `#8a693d`, sombra
`0 20px 60px rgba(33,24,15,.08)`, raios de 14/20/28px. O fundo repete o mesmo
brilho radial dourado sobre degradê. A UI saiu do slate escuro para papel claro.

Duas fontes, **servidas do binário**: Cormorant Garamond nos títulos e números
de destaque, Inter no corpo — as do site. São ~205 KB embutidos por `//go:embed`,
pela mesma razão dos bundles UMD: uma UI que depende do Google Fonts troca de
tipografia no meio da tela quando a rede não responde.

Cores de estado **dessaturadas** (`#4c7a56`, `#b0503c`, `#3f6d8f`, `#b3822f`):
verde e vermelho de painel puro brigam com o pergaminho e gritam mais alto que a
informação. O estado também carrega forma — ponto cheio, anel, pulso — para não
depender só de cor.

### Overview

Quatro indicadores (taxa de sucesso, taxa de falha, em execução, na fila), o
gráfico de execuções por hora, a rosca de distribuição, e as tabelas
"em andamento" e "próximas execuções".

- **A taxa exclui o que ainda corre do denominador.** Contar uma run em andamento
  como "não-sucesso" faria a taxa despencar durante um pico e subir sozinha
  depois, sem que nada tivesse mudado.
- **Gráficos são SVG gerado no servidor**, sem biblioteca — duas formas simples
  sobre dados que já estão na mão. O SSR continua valendo com JavaScript
  desligado.
- **O `generate_series` no SQL inclui as horas vazias.** Sem ele, uma hora sem
  execução não apareceria e o gráfico comprimiria o tempo, dando aparência de
  atividade contínua onde houve um buraco.
- **A curva de duração tem escala própria** (normalizada pelo próprio pico, com
  o pico anotado) e **corta no vazio**: ligar dois picos por cima de uma hora sem
  execução inventaria duração.
- **O próximo disparo é calculado pelo domínio `schedule`**, não por SQL:
  reimplementar cron no banco criaria uma segunda interpretação do mesmo campo,
  que um dia divergiria da que o scheduler usa.

### Workflows — a lista no formato Airflow

Busca, filtros por último estado, ativo/pausado e tag; colunas Workflow, Agenda,
Próxima execução, Última execução, Tags; interruptor à esquerda e "executar
agora" à direita.

Três coisas passaram a ser **reais** em vez de decorativas:

| elemento | o que faz |
|---|---|
| `tags:` no YAML | novo campo do spec, normalizado (apara, descarta vazias, dedup) |
| interruptor | `POST /workflows/{slug}/toggle` → `schedules.ativo` |
| ▶ executar agora | `POST /workflows/{slug}/trigger` → run manual enfileirado |

- **Efeitos por POST, nunca GET.** Um link que pausa a agenda seria disparado por
  qualquer prefetch de navegador.
- **Os dois são forms, sem JavaScript.** A tela inteira continua operável com o
  JS desligado, e o 303 pós-POST evita o "reenviar formulário?" ao atualizar.
- **O disparo manual usa o MESMO scheduler**, via `Scheduler.Disparar`. A UI não
  ganhou caminho próprio para criar Run: a regra da §37 continua com um dono só.
  Chave de idempotência com o segundo do clique, então dois cliques seguidos
  viram um run.
- **Pausar não cancela o que já está na fila**: runs materializados são trabalho
  aceito.
- **"Pausado" ≠ "sem agenda".** O filtro separa os dois; juntá-los esconderia
  justamente a agenda que alguém desligou.
- **A lista de tags não encolhe conforme se filtra** — se encolhesse, não haveria
  como voltar de um filtro para outro.
- O filtro roda **em memória**, sobre dezenas de linhas. Vira predicado no banco
  se um dia forem milhares.

### Verificação

Renderizado em Chrome headless e conferido nas quatro telas com dados reais:
Overview (27 execuções, 92,6% de sucesso, eixo 0/10/20/30/40), Workflows (8
workflows, um pausado, tags clicáveis), Execuções e a DAG — esta repintada na
mesma paleta, com nós em papel e arestas em ouro.

Ações exercitadas de ponta a ponta: o interruptor grava no banco e volta para a
URL filtrada de origem; o disparo cria run `manual` sem `logical_date` e
redireciona para ele; três cliques no mesmo segundo produziram **um** run.

Onze testes novos: filtros e cálculo do próximo disparo (`ui_internal_test.go`),
matemática dos gráficos (`charts_test.go`) — incluindo o caso de uma falha entre
400 sucessos, que precisa continuar visível.

### Limitações que este redesign não cobre

- Sem paginação nem ordenação por coluna na lista (o Airflow tem as duas).
- Sem "favoritar" workflow, sem exclusão pela tela.
- O gráfico é por hora, sem seletor de janela — 24h fixas.
- A lista de execuções segue nas 100 mais recentes.


---

## Cinco melhorias pós-redesign (2026-09-01)

### 1. Paginação e ordenação na lista de workflows

Colunas Workflow, Agenda, Próxima e Última execução são clicáveis: primeiro
clique ordena crescente, segundo inverte, terceiro remove a ordenação. Rodapé com
"51–55 de 55" e janela de cinco páginas.

Ordem, direção e página vivem na **URL** — uma tela ordenada pode ser colada para
outra pessoa. Trocar qualquer filtro volta para a página 1: continuar na página 7
de um resultado que agora tem duas seria uma tela vazia sem explicação.

Duas regras que a inversão ingênua (comparar com os argumentos trocados) quebrava,
e que os testes fixam:

- **O valor ausente fica por último nas duas direções.** Ordenar por "última
  execução" não pode começar por quem nunca rodou.
- **O desempate pelo slug é sempre crescente**, senão duas linhas equivalentes
  trocam de lugar a cada carregamento.

`/runs` ganhou o mesmo tratamento, mais filtros por estado, workflow e período —
paginados no banco (`LIMIT/OFFSET` com `count(*)` do mesmo predicado), porque o
histórico cresce sem limite.

### 2. O run que ficava na fila para sempre — dois bugs

**a) Ninguém executava.** A API não executa nada por decisão (§37: quem cria não
é quem executa), e a stack local não subia worker nenhum. O botão "executar
agora" criava o Run, ele entrava na fila e ficava lá. Agora o `docker-compose`
sobe um serviço `scheduler`.

Isso exigiu **dois alvos no Dockerfile**: `api` continua distroless (não executa
nada, superfície mínima), mas o worker roda os passos `run:` dos workflows — e
isso exige um shell. Rodar o scheduler na imagem distroless deixaria todo run
falhando com "no such file or directory": correto e incompreensível.

**b) Órfãos ficavam pendurados.** `Queue.Recuperar` existia desde a PHASE 2 e
ninguém a chamava — a dívida que a PHASE 6 tornou visível na tela. O dispatcher
ganhou um laço de recuperação (ticker próprio, um minuto), e `Recuperar` passou a
devolver os **itens**, não a contagem: com a contagem sozinha o item voltava para
a fila mas o Run seguia `running` para sempre — metade do bug.

O órfão é tratado como **falha daquela tentativa**, não como reenfileiramento
direto. Dois motivos: a máquina de estados não tem aresta `running → queued`
(§7), e um worker que morre no meio consumiu uma tentativa de verdade —
contabilizá-la é o que impede um run venenoso de derrubar workers em ciclo.
Esgotadas as tentativas, ele para em `failed` em vez de circular.

**Efeito colateral descoberto na hora:** com um scheduler de verdade rodando, os
testes de integração passaram a competir com ele pela fila — o critério de aceite
da PHASE 2 falhou com `queued: 87` em vez de 95, sem nada estar errado. `make
test-int` agora usa um banco próprio (`brevis_test`), criado e migrado por
`make test-db`.

### 3. Erro em diálogo, não em linha extra

O erro ocupava uma linha inteira abaixo da execução, dentro da tabela: com stack
trace de verdade isso empurrava o resto da lista para fora da tela e misturava a
lista com o detalhe. Agora a linha guarda um chip "erro" e o texto vive num
`<dialog>` nativo — ESC fecha, o backdrop escurece, o foco fica preso dentro dele
sem uma linha de JavaScript nossa.

- O chip é um `<a href="#erro-<id>">`, não um `<button>`: **o erro ganhou
  endereço**. Copiar o link leva outra pessoa direto para a falha, e o script
  abre o diálogo ao carregar a página com esse hash.
- Os `<dialog>` ficam **fora** da tabela: `<dialog>` dentro de `<tbody>` é HTML
  inválido e o navegador o move sozinho, quebrando o layout da linha.
- `<form method="dialog">` fecha sem JavaScript.
- O reset do Tailwind zera a margem de todo elemento, e o `<dialog>` perdia o
  `margin: auto` que o centraliza — nascia colado no canto superior esquerdo.

### 4. Respiro no cabeçalho das tabelas

`th` passou de `pb-2` para `px-3 py-4`. Colado no título, o cabeçalho competia
com a primeira linha em vez de separar dela.

### 5. Gráficos vivos

- **Tooltip próprio**, na hora, com a tipografia da página. O `<title>` do SVG
  até mostra o valor, mas só depois de um segundo parado e com a aparência do
  sistema operacional.
- **A coluna inteira é o alvo**, não só a barra: com uma única execução a barra
  tem 2px de altura, e exigir a mira exata tornaria o gráfico decorativo.
- **Clique leva à lista filtrada.** A barra abre `/runs?de=…&ate=…` daquela hora;
  a fatia da rosca abre `/runs?estado=…`. É o que transforma o gráfico em ponto
  de partida de investigação: ver o pico de falhas e clicar nele, em vez de
  reconstruir o filtro à mão. A tela de execuções mostra o recorte como chip, com
  o × para removê-lo — um filtro que só se tira mexendo na URL não é filtro, é
  armadilha.

Verificado: `/runs?de=2026-09-01T03:00:00Z&ate=…` abre com "período: 01/09
03h–04h · 30 execuções". A dica foi exercitada com o script real num harness com
o CSS da aplicação.

### Correções que apareceram no caminho

- **`jsonb_array_elements_text` derrubava a lista inteira** (HTTP 500) quando um
  workflow tinha `Tags` como JSON null — publicado antes do campo existir, ou sem
  tags. Uma linha quebrava a página toda. O `CASE` normaliza para array vazio
  antes de expandir.
- **`estado=` inválido** virava lista vazia com todos os chips apagados, que se
  lê como "não há execuções" em vez de "esse filtro não existe". Agora só estados
  da §7 são aceitos.
- **Guarda nos listeners de `ui.js`**: `closest` não existe em todo alvo de
  evento; sem ela um TypeError mataria o listener para o resto da sessão.


---

## Erro ilegível ao rodar um workflow (2026-09-01)

Relato: rodar `daily-report` terminava em

```
step "fetch_data": saiu com codigo 127
```

Tecnicamente correto e inútil. O comportamento estava certo — o passo é
`python fetch.py`, e não há `python` na imagem do worker — mas a mensagem não
dizia isso. **A causa existia e era jogada fora**: `/bin/sh: python: not found`
passava pelos eventos como log de stderr e era descartada ali mesmo; só o código
de saída chegava ao banco.

Três correções:

**1. O stderr acompanha a falha.** O runner mantém uma janela deslizante das
cinco últimas linhas de stderr e as anexa ao `ErroDePasso`. Coletar sempre, e não
só depois de falhar, é o ponto: quando o evento de falha chega, as linhas que o
explicam já passaram. Cinco linhas cobrem uma stack trace curta sem afogar a
coluna de erro do banco — e a causa quase sempre está no fim.

**2. Códigos de saída ganharam tradução.** 127 não é erro da aplicação, é comando
inexistente — a diferença entre procurar defeito no código e procurar na imagem.
Também 126 (sem permissão), 130/137/143 (sinais) e -1.

O mesmo erro, agora:

```
nivel 1: step "fetch_data": saiu com codigo 127 (comando nao encontrado —
verifique se ele existe na imagem do worker)
/bin/sh: python: not found
```

**3. `action:` não registrada explica melhor.** `docker.run` e `kubernetes.run`
estão no plano mas não existem ainda; a mensagem dizia "disponiveis: []", que
parece erro de digitação no nome. Agora diz que nenhuma ação foi registrada neste
worker e aponta o `run:` como alternativa.

**Os exemplos passaram a ser honestos.** `daily-report.yaml` e `analytics-dag.yaml`
ganharam cabeçalho dizendo que são ilustrativos e por quê; `examples/hello.yaml`
é novo e **roda em qualquer lugar** — quatro passos de shell, dois deles em
paralelo, para provar a stack ponta a ponta. Verificado: `hello` termina em
sucesso com os dois nós paralelos lado a lado no grafo.

Três testes novos: código e stderr na mensagem, janela limitada às últimas
linhas, e stderr em passo bem-sucedido não virando falha — muito comando legítimo
escreve progresso em stderr.


---

## Marca branca — identidade por configuração (2026-09-01)

Título, subtítulo, frase e paleta saem de um YAML. O objetivo é o produto: cada
cliente com a própria cara, **exceto** a linha "Powered by Brevis".

### Onde fica

`brand.example.yaml` na raiz é o modelo; copie para `brand.yaml` (ignorado pelo
git — é do cliente, não do repositório) ou aponte `BREVIS_BRAND_FILE`.

YAML e não banco, painel ou vinte variáveis de ambiente: workflows já são YAML, e
um segundo mecanismo de configuração seria um jeito novo de fazer a mesma coisa.

```yaml
titulo: Acme Dados
subtitulo: Plataforma
frase: |
  Dados confiáveis
  não acontecem por acaso.
tema:
  fundo: "#0f1720"
  tinta: "#e8eef5"
  destaque: "#4aa8c9"
```

### Como as cores chegam na tela

Sem recompilar CSS. **Todo utilitário do Tailwind resolve a cor por
`var(--color-*)`** — verificado: zero classes com hexadecimal fora de um
`var()`. Então o layout injeta um `<style>` no `<head>` sobrescrevendo as
variáveis, e a interface inteira se repinta.

Consequências que isso obriga a tratar:

- **Os gráficos SVG passaram a usar `var(--color-state-*)`** nos atributos
  `fill`/`stroke`, incluindo as cores que vinham do Go para a rosca. Hexadecimal
  ali faria os gráficos serem a única parte da tela a ignorar o tema.
- **A ilha da DAG lê as variáveis do CSS** com `getComputedStyle`, uma vez no
  carregamento — resolver a cada nó re-renderizado forçaria recálculo de estilo
  no meio do desenho. Onde ela compunha transparência concatenando alfa ao
  hexadecimal, agora usa `color-mix`: `var(--x)1f` não é cor nenhuma.
- **Os controles do React Flow ganharam override.** O bundle traz branco fixo e é
  carregado depois da folha compilada, então vencia por ordem — um tema escuro
  ficava com um bloco branco no canto do grafo. Seletores duplos resolvem.
- **`--color-line` e o realce dourado são derivados** de `tinta` e `destaque`,
  com alfa calculado no servidor. Não há campo para eles de propósito: uma borda
  que não combina com a própria tinta escolhida é um erro que ninguém deveria
  poder cometer.
- **Tema padrão não emite `<style>` nenhum** — repetir os valores que a folha
  compilada já tem seria bytes em toda página para não mudar nada.

### Segurança, não purismo

As cores são validadas contra `^#([0-9a-f]{3}|{6}|{8})$` e **qualquer outra coisa
é recusada**. Os valores vão para dentro de um bloco `<style>`: uma string livre
ali poderia fechar a declaração e injetar CSS arbitrário — que, num painel de
operação, é capaz de esconder um estado de falha atrás de um seletor. O teste
tenta exatamente isso (`"#fff;} body{display:none} .x{color:#fff"`).

### Falhas não derrubam a interface

- **Arquivo ausente é o caso normal** (instalação padrão não tem nenhum) — não é
  erro.
- **Campo ausente herda o padrão**: um arquivo com só `titulo:` é válido.
- **YAML quebrado ou cor inválida** viram aviso no log e a instalação sobe com o
  tema padrão. Derrubar a API por causa de uma cor seria pior que servi-la certa.

### A atribuição

`branding.Atribuicao` é uma constante do pacote, renderizada direto no rodapé da
barra lateral. Não é campo da struct `Marca`, então **não existe YAML capaz de
removê-la** — um teste garante que acrescentar `atribuicao:` ou `powered_by:` ao
arquivo não muda nada.

### Verificação

Um tema escuro completo (`Acme Dados`, azul-petróleo) aplicado por arquivo,
conferido em Overview e na tela da DAG: cartões, gráficos, rosca, nós, arestas e
controles do grafo — todos seguiram, com "POWERED BY BREVIS" no rodapé. O tema
padrão, no mesmo binário, continua idêntico e sem `<style>` extra.

Nove testes em `internal/branding`.


---

## Variáveis não chegavam na task (2026-09-01)

Relato: `dbt` falhando com `Env var required but not provided: 'GOOGLE_PROJECT_ID'`
mesmo com a variável no `.env` e no ambiente do container do scheduler.

Causa: **a task não herda o ambiente do orquestrador** — e isso é deliberado. O
processo do Brevis carrega `BREVIS_DATABASE_URL` com usuário e senha do Postgres,
e um workflow é um comando arbitrário escrito por outra pessoa; herdar por padrão
entregaria a credencial do banco a todo passo de todo pipeline. O executor local
passava apenas `PATH` e `HOME`.

O que faltava era o mecanismo de declarar o que a task precisa. Em Kubernetes ele
já existia (`BREVIS_POD_ENV_FROM_SECRETS` → `envFrom.secretRef`, sem passar pelo
scheduler); no modo local, não havia equivalente.

`BREVIS_TASK_ENV` preenche a lacuna:

| forma | efeito |
|---|---|
| `GOOGLE_PROJECT_ID,STAGE` | repassa essas do ambiente do processo |
| `STAGE=prod` | define um literal |
| `*` | repassa tudo **menos** as `BREVIS_*` |

A exceção do curinga é o ponto: a configuração do orquestrador nunca é trabalho
da task. Variável nomeada mas ausente **não vira string vazia** —
`GOOGLE_PROJECT_ID=""` faria o dbt falhar mais tarde, com mensagem pior que a de
variável ausente. E o scheduler agora avisa no boot quando as tasks recebem só
`PATH` e `HOME`, porque o erro resultante não aponta para a causa.

### A causa estava na tela e não chegava ao banco

O mesmo relato expôs um segundo defeito. A captura de contexto anexava à falha
apenas as últimas linhas de **stderr** — e o dbt imprime `Parsing Error` em
**stdout**. O histórico guardava `step "run": saiu com codigo 2`, sem a
explicação que passou pelo terminal.

Agora stderr tem precedência e stdout entra na ausência dele. Verificado na stack
real:

```
nivel 1: step "run": saiu com codigo 2
Running with dbt=1.10.3
Parsing Error
Env var required but not provided: GOOGLE_PROJECT_ID
```

Publicado em `daniel3843/brevis:0.1.1` (variáveis) e `:0.1.2` (stdout na falha).
Confirmado ponta a ponta: `brevis_smoke` roda `dbt debug` contra o BigQuery de
dev e termina com `All checks passed!` em 8 segundos.
