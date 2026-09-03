# SDK ← engine: contexto de execução sem passar pelo consumidor

**Aberto em** 2026-09-03 · **Base** `sdk/v0.9.1` · **Alvo** `sdk/v0.10.0`

> **EXECUTADA em 2026-09-03.** Os dez itens do §9 estão feitos, com prova para
> cada um. A prova do que o consumidor enxerga vive em `examples/consumer/`,
> fora do módulo do SDK, e roda no CI.
>
> **Correção de 2026-09-03:** o §7 está completo, e a ressalva anterior estava
> errada. Eu havia registrado que "nada instancia o `Runner`" — na verdade o
> `executar` do `cmdScheduler` sempre fez isso; ele tinha sumido junto com o
> entrypoint do engine, que um CLI escrevera por cima. Com o `cmd/bravis`
> recuperado, a ligação dispatcher → `Runner` reapareceu inteira, e só faltava
> passar os três campos novos (`Historico`, `Trigger`, `LogicalDate`).
>
> Duas coisas apareceram ao provar de ponta a ponta com `bravis run`:
>
> - O `RunID` zero era injetado como `BRAVIS_RUN_ID`, e o SDK decide estar sob
>   o engine pela **presença** do id — então um fetcher rodado à mão logaria
>   "running under Bravis" com um id inventado. Agora as três variáveis de
>   identidade só saem quando há run de verdade; os params continuam indo,
>   porque `--param` é como se passa entrada nesse caminho.
> - A tentativa é **0-based** no engine, como a coluna `task_runs.attempt`. A
>   documentação do SDK dizia que contava de 1.

Prompt de execução. O objetivo é o engine conseguir dizer ao SDK coisas que o
fetcher não sabe — é a primeira execução? quais os parâmetros deste disparo? —
**sem que o autor do fetcher escreva uma linha para receber isso**.

Um fetcher continua sendo:

```go
func main() {
    sdk.Run(sdk.Pipeline{
        Source: sdk.Source{...},
        Target: sdk.Target{...},
    })
}
```

Rodando sozinho, na mão, é exatamente isso. Rodando dentro do Bravis, o mesmo
binário passa a saber que é a primeira execução e cria a tabela — sem uma linha
a mais.

---

## 1. O que existe hoje, verificado

| peça | onde | estado |
|---|---|---|
| Canal de injeção | `internal/execution/executor.go:63` — `TaskExec.Env map[string]string` | existe; vira `env` no pod (`internal/execution/kubernetes/pod.go:248`) e no processo local |
| Quem preenche | `internal/application/execution/runner.go:389` — `Env: r.Env` | preenche do **runner**, não do run: nada por-execução chega ao passo |
| Dados do run | `internal/domain/run/run.go` — `Params`, `Attempt`, `TriggerType`, `LogicalDate`, `IdempotencyKey` | existem no domínio, não saem dele |
| Precedência no SDK | `sdk/config.go` — explícito › env › default › erro, com log de origem | existe e funciona |
| `CreateTable` | `sdk.Target.CreateTable bool` | opt-in, default `false` |

**O buraco é um só:** o `runner` monta `TaskExec` com o `Env` do runner e
descarta tudo que sabe sobre *este* run. `Params`, `Attempt` e `LogicalDate`
morrem em `runner.go`.

---

## 2. O canal: variáveis de ambiente, e por que não outra coisa

O engine já injeta ambiente em pod e em processo local, pelo mesmo campo. Um
segundo mecanismo — arquivo, socket, flag — significaria dois caminhos para
testar e dois para quebrar.

**Prefixo `BRAVIS_RUN_`**, separado do `BRAVIS_SDK_` que já existe: um diz *o
que o SDK faz*, o outro *o que este disparo é*. Misturá-los faria
`BRAVIS_SDK_DATASET` e `BRAVIS_RUN_PARAMS` parecerem a mesma categoria de
coisa, e não são.

| variável | tipo | vem de |
|---|---|---|
| `BRAVIS_RUN_ID` | UUID | `Run.ID` |
| `BRAVIS_RUN_FIRST` | `"true"` \| `"false"` | §3 |
| `BRAVIS_RUN_ATTEMPT` | inteiro | `Run.Attempt` |
| `BRAVIS_RUN_TRIGGER` | `schedule` \| `manual` \| `backfill` | `Run.TriggerType` |
| `BRAVIS_RUN_LOGICAL_DATE` | RFC 3339, vazio em disparo manual | `Run.LogicalDate` |
| `BRAVIS_RUN_PARAMS` | JSON `{"chave":"valor"}` | `Run.Params` |

> **Diga a verdade sobre isto na documentação.** Variável de ambiente **não é
> canal privado**: o processo do fetcher pode lê-la, e alguém vai ler. O que se
> promete é que ele **não precisa** — não que não consegue. Prometer isolamento
> aqui seria mentir, e a primeira pessoa que rodar `os.Environ()` descobre.

---

## 3. Quem decide `firstRun`, e por que não o SDK

Três candidatos, e dois estão errados:

| quem decide | como | veredito |
|---|---|---|
| **SDK**, "a tabela não existe" | consulta o destino | **não.** Confunde *primeira execução* com *tabela ausente*. Alguém apaga a tabela por engano e a próxima execução se acha a primeira |
| **Engine**, "primeiro run bem-sucedido deste workflow" | consulta `runs` | **sim.** É a única que tem o histórico |
| Consumidor, por flag | `--first-run` | **não.** É exatamente o que se está tirando dele |

**A consulta:** existe algum `run` deste `workflow_slug` com status terminal de
sucesso, anterior a este? Se não, `BRAVIS_RUN_FIRST=true`.

Duas armadilhas que custam caro se ignoradas:

- **Retry não é primeira execução de novo.** `Attempt > 1` do mesmo run não
  volta a ser primeira. Decida por *run*, não por tentativa.
- **Por workflow ou por step?** Um workflow com três fetchers escrevendo em três
  tabelas: se `firstRun` for do workflow, o segundo fetcher recebe `false` na
  primeira vez que roda, e a tabela dele não é criada. **Tem de ser por step.**
  Grave o par (`workflow_slug`, `step_id`) — sem isso o recurso funciona no
  workflow de um passo só e falha silenciosamente no resto.

**Como provar:** teste que insere runs anteriores e confere que
`BRAVIS_RUN_FIRST` só é `true` quando não há sucesso prévio *daquele step*; e um
que confere que a segunda tentativa do mesmo run não reabre a flag.

---

## 4. Precedência: quem ganha quando o código e o engine discordam

Aqui está a decisão que decide o resto.

`Target.CreateTable` é `bool`. O zero value é `false`, e `false` significa duas
coisas incompatíveis: "não quero criar" e "não falei nada". Se o engine mandar
`firstRun=true` e o campo estiver zerado, não dá para saber se o autor recusou
ou se calou.

**A saída é tri-estado**, como o `Data.Stats()` não precisou ser e este precisa:

```go
CreateTable *bool   // nil = não falei; o engine decide
```

| código | engine | resultado |
|---|---|---|
| `nil` | `firstRun=true` | **cria** |
| `nil` | `firstRun=false` | não cria |
| `sdk.Bool(true)` | qualquer | **cria** — o autor mandou |
| `sdk.Bool(false)` | `firstRun=true` | **não cria** — o autor recusou, e recusa explícita ganha |

Ponteiro incomoda. A alternativa — o engine sempre ganhar — significa que um
`CreateTable: false` escrito de propósito é ignorado dentro do Bravis e
respeitado fora, o que é pior: o mesmo código com dois comportamentos e nenhum
aviso.

**Regra geral, e escreva-a na documentação:** explícito no código › injetado
pelo engine › variável de ambiente › default › erro. O `sdk/config.go` já loga a
origem de cada valor; **`(do engine)` entra como origem**, senão a primeira
pergunta de plantão — "por que ele criou a tabela?" — não tem resposta no log.

---

## 5. `propertyRun`: os parâmetros do disparo

`BRAVIS_RUN_PARAMS` chega como JSON e vira um mapa que o fetcher **pode** ler,
sem precisar:

```go
sdk.Run(sdk.Pipeline{
    Before: func(ctx context.Context, p *sdk.Pipeline) error {
        if p.Run.Params["load_full"] == "true" {
            p.Source.URL += "&full=1"
        }
        return nil
    },
})
```

Duas coisas que o `Run` precisa expor, e nada além por enquanto:

```go
type RunContext struct {
    ID          string
    First       bool
    Attempt     int
    Trigger     string
    LogicalDate time.Time
    Params      map[string]string
}
```

**`createTable` dentro de `propertyRun`** — o pedido original — é um parâmetro
como qualquer outro, com um significado que o SDK conhece:
`params["create_table"] == "true"` liga a criação, como se fosse `firstRun`.
Isso dá ao operador um botão para o caso "a tabela foi apagada, recria" sem
editar código nem inventar uma primeira execução falsa.

**Como provar:** teste que roda o mesmo `Pipeline` com e sem
`BRAVIS_RUN_PARAMS`, conferindo que o `Before` enxerga os valores e que o
fetcher sem `Before` não muda de comportamento.

---

## 6. Rodar sem engine continua sendo o caso padrão

Nada disto pode ser obrigatório. Sem nenhuma variável:

- `RunContext` vem zerado, `First` é `false`, `Params` é um mapa vazio — **não
  nulo**, para `p.Run.Params["x"]` não estourar;
- `CreateTable` nil significa não criar, que é o comportamento de hoje;
- nenhum log a mais, nenhum aviso. Um fetcher rodando na mão não deve nem
  perceber que este mecanismo existe.

**Como provar:** o teste que hoje roda o `Pipeline` inteiro sem ambiente algum
tem de continuar passando sem uma linha de mudança. Se precisar mudar, o recurso
vazou.

---

## 7. O que muda no engine

Um lugar, e é pequeno:

**`internal/application/execution/runner.go:389`** — hoje `Env: r.Env`. Passa a
mesclar o ambiente do runner com o do run:

```go
Env: mesclar(r.Env, contextoDoRun(run, step)),
```

`contextoDoRun` monta as seis variáveis da §2. **O ambiente do runner ganha em
colisão** — se alguém definiu `BRAVIS_RUN_PARAMS` na configuração do runner, é
porque quis, e o engine não deve sobrescrever configuração explícita.

O executor Kubernetes e o local não mudam: os dois já leem `TaskExec.Env`.

**Como provar:** teste de `runner` que monta um `TaskExec` e confere as seis
variáveis; e um teste de `pod` que confere que elas chegam ao container.

---

## 8. O que **não** deve entrar

- **Segredo por esta via.** `BRAVIS_RUN_*` é contexto de execução, não
  credencial. Credencial continua em `envFrom.secretRef`, que já existe.
- **Objeto grande.** Se `BRAVIS_RUN_PARAMS` crescer para além de alguns KB, o
  canal está errado — variável de ambiente tem limite e o erro aparece como pod
  que não inicia, longe da causa.
- **O SDK consultando o banco do engine.** O SDK não conhece Postgres e não deve
  passar a conhecer. Ele recebe o que o engine decidiu; ele não decide.
- **Campo público sem implementação.** Se `Trigger` ou `LogicalDate` não forem
  lidos por nada nesta versão, **não entram na struct**. Este projeto já pagou
  esse preço quatro vezes; a quinta não tem desculpa.

---

## 9. Critério de pronto

1. Um fetcher **sem uma linha de mudança** cria a tabela na primeira execução
   dentro do Bravis, e não cria fora.
2. `firstRun` é por (`workflow_slug`, `step_id`), não por workflow, e um retry
   não o reabre.
3. `CreateTable` é tri-estado, e `sdk.Bool(false)` explícito vence o engine.
4. `sdk/config.go` loga `(do engine)` como origem, e a mensagem diz qual
   variável trouxe o valor.
5. `params["create_table"] == "true"` liga a criação sem simular primeira
   execução.
6. `RunContext.Params` nunca é nulo.
7. Rodar o SDK sozinho não exige variável nenhuma, e o teste que prova isso é o
   que já existe, sem alteração.
8. Nenhum campo em `RunContext` que nada leia.
9. `go build ./...` e `go vet ./...` verdes em `sdk`, `examples` **e
   `cmd/bravis`** antes da tag.
10. A documentação diz, com estas palavras, que variável de ambiente não é canal
    privado: o fetcher não **precisa** lê-las, não que não **consegue**.

---

## 10. Armadilhas já pagas neste repositório

Estão aqui porque cada uma custou uma investigação, e todas reincidiriam neste
recurso se ninguém olhar.

| armadilha | como apareceu |
|---|---|
| **Campo declarado que nada lê** | quatro vezes: seis campos do `extract` na v0.1.1, `MetadataNamespace`, `SourceKeyField`, `Result.Pages`/`Attempts` |
| **Função escrita e nunca chamada** | `applyLayout` — `CreateTable` teria sido flag sem efeito, pega pelo linter e não por raciocínio |
| **Símbolo inalcançável de fora** | três `With*` no `internal/core` sem re-export: compilava, tinha teste, e nenhum consumidor podia chamar |
| **Teste que prova de dentro** | os contadores passavam usando `data.stats`; o acessor público não existia. Testar de dentro não prova o que o usuário consegue |
| **CI que não constrói o artefato** | `cmd/bravis` quebrou numa renomeação e o CI ficou verde, porque ninguém o construía |
| **Verificação que não pode falhar** | `|| true` nos examples; URL do proxy sem case-encoding saindo `exit 0` |

**A regra que sai daí:** todo item deste plano precisa de uma prova que
**falharia** se ele não existisse — e a prova do que o consumidor enxerga tem de
ser escrita de fora, num módulo separado, não de dentro do pacote.
