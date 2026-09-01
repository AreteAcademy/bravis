# PHASE 4 — SCHEDULER

Concluída em 2026-09-01.

## Implemented

| item | onde |
|---|---|
| Cron e timezone | `internal/domain/schedule` |
| Política de catchup | idem |
| Missed runs | idem + `internal/scheduler/scheduler.go` |
| Backfill | `Scheduler.Backfill`, `bravis backfill` |
| Manual trigger | `bravis run` (PHASE 1) e `trigger_type` |
| `trigger_type` (§12) | `runs.trigger_type` — schedule, manual, backfill, api, retry |
| Publicação de workflow | `bravis publish`, `workflows.definicao` |

## A regra estrutural da fase

A §37 é categórica: *"Scheduler creates runs. Queue executes runs. Nunca misturar
responsabilidades."*

Está separado de fato: `Scheduler.Ciclo` materializa slots em Runs e os enfileira,
e nada mais. Quem executa é o `Dispatcher` da PHASE 2, consumindo a fila. Os dois
laços correm independentes no `bravis scheduler` — **o scheduler pode cair sem
interromper nenhuma execução em voo, e o dispatcher pode cair sem perder nenhum
slot.**

## Architecture Decisions

**A chave de idempotência é `slug:trigger:slot`.** É o que torna o scheduler
seguro sob reinício: se ele cair depois de criar o Run e antes de avançar o
marcador, a tentativa seguinte colide na `unique` em vez de duplicar. É o caso
exato da §29, resolvido por constraint e não por lógica.

**O marcador avança a cada slot, não ao fim do laço.** Se o processo cair no meio
de uma lacuna, os slots já materializados não são recriados.

**O backfill não mexe no marcador.** Ele preenche o passado; avançar `ultimo_slot`
faria o scheduler pular slots futuros que ainda não aconteceram.

**Backfill entra na fila com prioridade −10.** A §12 exige que ele respeite
concorrência e prioridade. Negativa faz o backfill ceder a vez ao trabalho
corrente em vez de competir com ele.

**Republicar preserva o marcador.** O `ON CONFLICT DO UPDATE` atualiza cron,
timezone e catchup, mas **não** `ultimo_slot` — senão editar um YAML recriaria o
passado.

**Tirar o `schedule` do YAML desagenda.** Publicar sem cron remove a agenda, em
vez de deixar a antiga viva.

**Uma agenda com cron inválido não impede as outras.** O erro é registrado e o
ciclo continua.

## O bug que o e2e encontrou

Um teste ponta a ponta criou **1.100 runs onde deveria criar 1**. A causa:

```go
if limite > 0 && len(slots) > limite {
    return slots[:limite], true, nil   // <-- saía ANTES do filtro de catchup
}
```

O retorno antecipado da truncagem pulava o filtro de `catchup`. Consequência:
sempre que a lacuna passasse do limite por ciclo, **`catchup: false` se comportava
como `true`** — exatamente o desperdício que a opção existe para evitar, e de
forma silenciosa.

Corrigido: sem catchup, o cálculo nem acumula — percorre e devolve só o mais
recente, com teto de iterações para o caso de `ultimo_slot` muito antigo. Há teste
de regressão, e o e2e confirma: 8 meses de lacuna com `catchup=false` produzem 1
run.

Vale registrar o método: o bug **passou pelos testes unitários** porque eles
exercitavam catchup e truncagem separadamente, nunca juntos. Só a execução real
com estado sujo o expôs.

## Tests

`go test ./...` passa; os de integração pulam sem `BRAVIS_TEST_DATABASE_URL`.

- **domínio**: catchup true preenche a lacuna, false pega só o recente, limite
  trunca e sinaliza, agenda nova não cria histórico, timezone muda o instante
  (02:00 em São Paulo = 05:00Z), agenda inativa não produz, validação de cron e
  fuso, o cron do exemplo do autor, e a **regressão do catchup contornado**
- **integração**: cria e enfileira; ciclo repetido não duplica; catchup false não
  refaz o passado; backfill entra na fila com prioridade menor; backfill não
  avança o marcador; republicar preserva o marcador; publicar sem cron desagenda;
  backfill inclui o slot da borda (24 slots num dia, não 23)

## Benchmarks

Ainda não formalizados. Observado no e2e: o ciclo do scheduler sobre uma agenda
com 8 meses de lacuna e `catchup=false` resolve em milissegundos, porque não
acumula slots. Com `catchup=true` o custo é proporcional à lacuna, limitado pelo
teto por ciclo.

## Known Limitations

- **Uma agenda por workflow.** A §22 sugere N (um cron diário e outro de
  reconciliação); a `unique (workflow_slug)` precisa sair quando isso for
  necessário.
- **Timezone fixo em UTC na publicação.** O YAML ainda não tem campo de fuso,
  embora o domínio o suporte e seja testado.
- **`catchup` não vem do YAML.** É coluna no banco, com padrão `false`. A §12
  mostra `catchup: true` no arquivo — item pendente.
- **`Queue.Recuperar` continua sem laço que a chame.** Segue pendente desde a
  PHASE 2; o `bravis scheduler` seria o lugar natural.
- **Sem `trigger_type: api`** — não há API de disparo ainda.
- Os testes de integração e o e2e compartilham o banco; o helper trunca tudo, então
  rodá-los em paralelo interfere.

## Next Phase

**PHASE 5 — UI FOUNDATION**: templ, templUI e o servidor de páginas (§17). É o
primeiro pedaço da parte que a §1 chama de Bravis Observe.
