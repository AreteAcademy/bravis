# Contexto entre passos: o que um passo diz ao seguinte

**Escrito em** 2026-09-05 · **Base** motor `0.3.0`, `sdk/v0.42.1` · **Estado** proposta, não executável ainda

Pedido de quem consome:

> Vamos supor que eu montei um script em Go, e logo depois subi um script em
> Python, como podemos injetar contexto nas tasks? Via SDK isso precisa ser
> nativo.

Este documento é a resposta: o que o desafio realmente é, o que já existe, o
desenho proposto, e **o que dá errado em cada caminho** — que é a parte que
costuma faltar e a que o pedido pede em voz alta.

---

## 1. O que existe hoje, e o que falta

O motor já injeta contexto **dele** em toda task:

```
BREVIS_RUN_ID  BREVIS_RUN_FIRST  BREVIS_RUN_ATTEMPT
BREVIS_RUN_TRIGGER  BREVIS_RUN_LOGICAL_DATE  BREVIS_RUN_PARAMS
```

E, desde a `0.3.x`, `env:` e `secrets:` por passo — que é a instalação e o YAML
falando com a task.

**O que não existe é a task falando com a próxima.** Uma direção só, e sempre de
cima para baixo.

Na DAG do pedido:

```yaml
steps:
  - id: ingest_users
    action: kubernetes.run
    with: {image: company/ingestion:latest}

  - id: transform_silver
    run: brevis run --select silver.*
    depends_on: [ingest_users]

  - id: gold_metrics
    run: brevis run --select gold.metrics
    depends_on: [transform_silver]

  - id: gold_users
    run: brevis run --select gold.users
    depends_on: [transform_silver]

  - id: publish
    run: ./publish.sh
    depends_on: [gold_metrics, gold_users]
```

`depends_on` diz **quando** um passo roda. Não diz **o quê** ele recebe. Hoje, se
o `ingest_users` descobre que a janela ingerida vai até `2026-09-05T04:00Z`, não
há como o `transform_silver` saber disso a não ser reconsultando o warehouse — e
essa reconsulta é uma segunda fonte da verdade que pode discordar da primeira.

E ela expõe as três formas do problema de uma vez:

| | |
|---|---|
| `ingest_users` → `transform_silver` | **linguagens diferentes**: um binário Go e um comando de dbt |
| `transform_silver` → `gold_*` | **um para muitos**, e os dois em paralelo |
| `gold_*` → `publish` | **muitos para um**, e o consumidor é um shell script |

Qualquer desenho que só resolva o primeiro caso não resolve nada.

---

## 2. As restrições, e por que elas eliminam quase todas as opções

### 2.1 Cada passo é um pod

Em Kubernetes, `transform_silver` e `gold_metrics` são processos em máquinas
possivelmente diferentes. Não há memória compartilhada, não há sistema de
arquivos compartilhado, e o segundo começa depois de o primeiro ter deixado de
existir.

**Isso elimina** qualquer coisa baseada em variável de ambiente escrita pela
task, em arquivo local, ou em retorno de função.

### 2.2 O motor não tem `pods/exec`

O `docs/KUBERNETES.md` é explícito: a conta `brevis-scheduler` tem *"criar, ler,
listar, observar e apagar pods; ler logs. Sem `update`, sem `patch`"*. E a conta
da task não tem permissão nenhuma.

**Isso elimina** o motor entrar no pod para buscar um arquivo, e elimina a task
chamar a API do Kubernetes para anotar um resultado.

### 2.3 As linguagens são as do consumidor

Go hoje, Python amanhã, um `publish.sh` no fim. O transporte precisa ser algo
que qualquer linguagem escreve com a biblioteca padrão dela.

**Isso elimina** qualquer formato que dependa do SDK — o SDK é *uma* das pontas,
nunca as duas.

### 2.4 O que sobra

Três caminhos, e o terceiro é o certo:

| caminho | por que não / por que sim |
|---|---|
| a task chama a API do motor | precisa de rede do pod até o motor, de autenticação, e de o motor estar de pé quando a task termina. Acopla o dado ao tempo de vida do servidor |
| volume compartilhado | precisa do volume configurado — o mesmo `PersistentVolume` da credencial —, e num RWX ele vira estado global entre execuções sem ninguém pedir |
| **mensagem de término do pod** | é do Kubernetes, existe para exatamente isto, **e o motor já a lê** |

---

## 3. O achado que decide o desenho

`internal/execution/kubernetes/pod.go:151` já parseia:

```go
Terminated *struct {
    ExitCode int    `json:"exitCode"`
    Reason   string `json:"reason"`
    Message  string `json:"message"`   // ← nunca lido
}
```

O Kubernetes lê `/dev/termination-log` do container quando ele termina — com
sucesso ou não — e põe o conteúdo em `containerStatuses[].state.terminated.message`.
O motor **já busca esse status**, porque é dele que ele tira o código de saída.

Então o transporte de volta existe, custa zero permissão nova, zero
infraestrutura, e vale para qualquer linguagem: é escrever um arquivo.

### O limite de 4 KB é uma vantagem, não um obstáculo

A mensagem de término é truncada em **4096 bytes**. Isso parece uma limitação e
é a melhor parte do desenho.

O modo como implementações de "contexto entre tarefas" falham é sempre o mesmo:
alguém põe **dado** onde só cabia **contexto**. O XCom do Airflow guarda no banco
de metadados, e a consequência conhecida é gente empurrando DataFrame por ali até
o banco do orquestrador virar o gargalo do pipeline.

Um teto que não é nosso é mais forte que uma política que teríamos de fazer
valer. Quatro quilobytes cabem uma marca d'água, uma contagem, uma lista de
partições. Não cabem os dados — e é exatamente essa a fronteira.

---

## 4. O desenho

### 4.1 O contrato, em duas variáveis

```
BREVIS_INPUT    JSON: {"<id do passo>": {<saídas dele>}}
BREVIS_OUTPUT   caminho de um arquivo onde escrever um objeto JSON
```

Em Kubernetes, `BREVIS_OUTPUT` é `/dev/termination-log`. Localmente, um temporário
que o motor lê depois. **A task não precisa saber a diferença.**

Um `publish.sh`, sem SDK nenhum:

```bash
#!/usr/bin/env bash
janela=$(jq -r '.ingest_users.watermark' <<< "$BREVIS_INPUT")
./publica --ate "$janela"
jq -n --arg u "$(date -Is)" '{publicado_em: $u}' > "$BREVIS_OUTPUT"
```

### 4.2 A declaração, no YAML

```yaml
steps:
  - id: ingest_users
    action: kubernetes.run
    with: {image: company/ingestion:latest}
    outputs: [watermark, rows]

  - id: transform_silver
    run: brevis run --select silver.*
    depends_on: [ingest_users]
    needs: [ingest_users.watermark]
    outputs: [modelos_construidos]

  - id: publish
    run: ./publish.sh
    depends_on: [gold_metrics, gold_users]
    needs: [gold_metrics.linhas, gold_users.linhas]
```

`outputs` diz o que o passo publica. `needs` diz o que ele exige.

**A regra é assimétrica, e é a mesma que o `Reconcile` do SDK já usa:**

| situação | resposta |
|---|---|
| `needs` algo que nenhum `depends_on` declara em `outputs` | **erro no `brevis validate`**, antes de qualquer coisa rodar |
| um `outputs` declarado que o passo não escreveu | **erro ao fim do passo** — ele disse que escreveria |
| uma chave escrita e não declarada | **erro** — senão ela vira dependência de alguém sem ninguém ter decidido |
| um `needs` que o passo não lê | ninguém sabe, e tudo bem: exigir é barato |

O `brevis validate` passar a pegar isso é o que torna a coisa utilizável. Um
`needs: [ingest_users.watermrak]` com o erro de digitação tem de falhar no
editor, e não às 4 da manhã no terceiro passo.

### 4.3 No SDK, nativo

Escrever:

```go
sdk.Run(sdk.Pipeline{
    Source: ..., Target: ...,

    // Roda depois do Load, com o Result na mão. O que ele devolve vira as
    // saídas do passo.
    Outputs: func(res *sdk.Result) (map[string]any, error) {
        return map[string]any{
            "rows":      res.Rows,
            "watermark": ultimoTS,
        }, nil
    },
})
```

Ler:

```go
// Erro nomeando o passo e a chave quando não há -- nunca string vazia.
janela, err := run.Upstream("ingest_users").String("watermark")
linhas, err := run.Upstream("gold_users").Int("linhas")
```

O `RunContext` já existe, já é montado das variáveis de ambiente e já chega ao
fetcher desde a `v0.10.0`. `Upstream` é mais um campo dele, montado de
`BREVIS_INPUT` — não é mecanismo novo, é o mesmo com mais uma fonte.

---

## 5. Os desafios, um a um

Esta é a seção que o pedido pede. Cada um destes já derrubou uma implementação
de "contexto entre tarefas" em algum orquestrador.

### 5.1 O truncamento silencioso

4096 bytes é um corte, não uma recusa. Um JSON cortado no meio de uma string não
é JSON — e a leitura ingênua trata isso como "o passo não escreveu nada".

**Dois guardas, em lugares diferentes, e os dois são necessários:**

1. o SDK **recusa antes de escrever** o que passa do teto, com erro nomeando o
   tamanho e as chaves — é ele que sabe o que está pondo lá;
2. o motor **distingue** "vazio" de "não parseia". Vazio é um passo que não
   publica nada, e é legítimo. Não parsear é erro, e a mensagem tem de dizer
   truncamento, porque é a causa em nove de dez casos.

Sem o segundo, um passo que escreve 5 KB some sem ruído nenhum.

### 5.2 Isto NÃO é estado entre execuções

Alguém vai querer `ingest_users.watermark` da execução de **ontem**. É o pedido
mais natural do mundo e é outra coisa.

Contexto entre passos vive dentro de um Run e morre com ele. Estado entre
execuções é persistência — o mesmo problema que a credencial rotacionada
resolveu com um store, e com as mesmas perguntas: onde vive, quem lê, o que
acontece quando duas execuções concorrem.

Confundir os dois faz o contexto virar um banco de dados acidental, sem
ninguém ter desenhado um. **A resposta tem de ser um "não" explícito**, com o
caminho certo apontado ao lado.

### 5.3 A retentativa, e o Run retomado

Duas perguntas que parecem uma:

**Um passo que falha e reexecuta** substitui a própria saída. A tentativa
anterior falhou; a saída dela não pode sobreviver.

**Um Run que retoma** é o caso que obriga a persistir. O `Historico` já responde
`PassoJaTeveSucesso`, então um Run retomado pula o `ingest_users` — e o
`transform_silver` precisa da saída dele mesmo assim.

Ou seja: **guardar em memória não basta.** A saída vai junto do registro da task,
no mesmo lugar onde já se sabe que ela teve sucesso.

### 5.4 O segredo que alguém vai pôr ali

Vai acontecer. Um token, uma URL assinada.

A mensagem de término está no status do pod, legível por qualquer `get pods` do
namespace, e a saída persistida está no histórico, legível por quem vê execuções.

**A posição precisa ser dita e não pode ser "o SDK impede"** — ele não tem como
saber que uma string é um token. O que dá para fazer:

- dizer, na documentação do campo, que aquilo é visível onde a execução é;
- e não oferecer atalho que convide: nada de `Outputs` que copie o ambiente.

### 5.5 O paralelo

`gold_metrics` e `gold_users` rodam ao mesmo tempo e ambos leem
`transform_silver`. Leitura concorrente do mesmo valor imutável não tem
problema.

Escrita, sim — e é resolvida por construção: a saída é **chaveada por id de
passo**, e dois passos não têm o mesmo id. O `Validate` já recusa id duplicado.

### 5.6 O tamanho do `BREVIS_INPUT`

Passar tudo o que já se sabe para todo passo funciona até a DAG ter cinquenta
passos e o `ARG_MAX` aparecer.

Passar **só o que o passo declarou em `needs`** resolve o tamanho e, mais
importante, torna a declaração carregada de verdade: um passo que lê algo que
não declarou não encontra, e o erro diz que falta declarar.

### 5.7 O passo que não é do SDK

O `action: kubernetes.run` do exemplo, e o `publish.sh`. Eles têm de conseguir
participar sem SDK, ou o desenho vira "funciona se você usar Go".

Por isso o contrato é **duas variáveis de ambiente e um arquivo**, e não um
formato nosso. O `jq` do exemplo é a prova: se não couber numa linha de shell,
está errado.

### 5.8 O modo local

`brevis run` não tem banco. O contexto entre passos precisa funcionar ali —
senão o único jeito de testar um pipeline com contexto é subir um cluster.

Em memória por Run, no processo, com o mesmo contrato de variáveis. O `5.3` só
vale onde há histórico.

---

## 6. O que NÃO fazer

- **Não** usar a saída padrão como transporte. Ela é log, e um `print()` de
  depuração no lugar errado corromperia o contexto. Um arquivo é inequívoco.
- **Não** aceitar valor sem declaração. Uma chave que aparece sozinha vira
  dependência de alguém, e ninguém decidiu.
- **Não** deixar o teto implícito. Um passo que escreve 5 KB tem de falhar
  dizendo isso, e não sumir.
- **Não** transformar isto em estado entre execuções. Ver 5.2.
- **Não** inventar tipos. O valor é JSON — número, texto, booleano, lista,
  objeto. Quem precisa de mais escreve no warehouse, que é onde dado mora.

---

## 7. Ordem sugerida, e o que cada fase destrava

| # | fase | por que nesta ordem |
|---|---|---|
| 1 | `outputs`/`needs` no YAML e no `brevis validate` | é declaração pura, não muda execução nenhuma, e já dá valor: pega o erro de digitação no editor |
| 2 | o motor lê a mensagem de término e persiste a saída | o transporte de volta, com o campo que já é parseado |
| 3 | `BREVIS_INPUT` montado do que o passo declarou | fecha o ciclo para qualquer linguagem |
| 4 | `Pipeline.Outputs` e `RunContext.Upstream` no SDK | o nativo que o pedido pede, sobre um contrato que já funciona sem ele |
| 5 | modo local | mesmo contrato, sem histórico |

A fase 4 vem depois de propósito. Se o SDK for a primeira, ele vira a referência
e o contrato nasce moldado nele — e aí o `publish.sh` é encaixado depois, mal.
Nascendo do lado do shell, o SDK é uma conveniência sobre algo que já vale sem
ele.

---

## 8. O que este documento não resolve

- **O tipo do valor não é declarado.** `needs: [ingest_users.watermark]` diz o
  nome, não que é um timestamp. Um passo que escreve número onde o outro espera
  texto falha no consumo, e não na declaração. Dá para resolver como o
  `Target.Schema` resolveu — e é escopo próprio, não deste.
- **4 KB pode não bastar** para uma lista de partições grande. A saída seria
  então uma *referência* — um caminho no object storage — e não o conteúdo. Isso
  funciona e é o padrão certo, mas precisa ser dito, senão cada consumidor
  descobre sozinho.
- **`action:` roda em processo**, então poderia devolver o mapa direto sem passar
  por arquivo. É uma otimização e uma inconsistência ao mesmo tempo; vale medir
  se importa antes de decidir.

---

## 9. O que a primeira conversa sobre isto mudou

Antes de construir o transporte, duas coisas apareceram e uma delas o dispensa
para o caso que motivou o pedido.

### O `BREVIS_RUN_ID` já é o token compartilhado

O caso concreto era: um extract em Go escreve um arquivo, um load em Go o lê. O
motor **já injeta `BREVIS_RUN_ID` em toda task** da execução, e o `from.Files`
aceita glob:

```go
// pod do extract
To: to.Files{Path: "s3://bucket/stage/" + os.Getenv("BREVIS_RUN_ID") + "/"}

// pod do load
From: from.Files{Path: "s3://bucket/stage/" + os.Getenv("BREVIS_RUN_ID") + "/*.ndjson"}
```

Nenhum mecanismo novo. E para esse caso é **melhor** que o contexto: sobrevive à
retentativa (mesmo run, mesmo prefixo), não tem teto de tamanho porque não
trafega nada, e um `publish.sh` faz igual com `$BREVIS_RUN_ID`.

**O contexto passa a valer quando o passo seguinte precisa de algo que só foi
descoberto na execução e não é derivável** — uma marca d'água que a fonte
devolveu, quais das 4.803 origens falharam, a versão de schema que o fornecedor
mandou hoje. Nenhum desses apareceu ainda.

Construir o transporte antes disso custaria o motor inteiro — ler a mensagem de
término, persistir, validar, montar o `BREVIS_INPUT` — para servir um caso que
uma variável de ambiente já serve. E o desenho ficaria moldado por um caso
hipotético, que é como se erra a forma.

### E o `to.Files` não dizia o que escrevia

Corrigido na `sdk/v0.43.0`, e vale por si: o driver escolhe o nome do arquivo, e
o log dizia `estrategia=file` sem dizer qual.

Escrever esse teste descobriu um defeito maior: `to.Files{Path: "s3://bucket/landing"}`,
sem barra no fim, escrevia em `s3://bucket/parte-...` — descartando o `landing`
como se fosse nome de arquivo, em silêncio.

### A lib de Node e Python não é um projeto

O contrato é duas variáveis de ambiente e um arquivo JSON. A "lib" para isso tem
umas vinte linhas em qualquer linguagem. O que precisa ficar bom é o contrato;
as libs são conveniência sobre ele — e planejá-las como entrega própria inverte
a ordem.
