# Gaps entre o YAML pedido e o `docs/plan.md`

O exemplo `daily-report.yaml` é concreto e utilizável — e diverge do plano em
pontos que **definem arquitetura**, não em detalhes de sintaxe. Este documento
lista as divergências, o que já foi resolvido e o que precisa de decisão.

O YAML em questão:

```yaml
schedule: "0 2 * * *"
type: chain
steps:
  - id: fetch_data
    run: python fetch.py
  - id: build_report
    action: docker.run
    with:
      image: ghcr.io/acme/reporting:1.4.2
      volumes: [~/data:/work/data]
      command: python report.py
  - id: notify
    run: ./notify.sh
```

---

## RESOLVIDOS nesta fase

### 1. `chain` contra o DAG do plano

O plano é DAG-first (§5, §6, editor React Flow). O YAML é uma cadeia linear.

**Resolvido como açúcar sintático**: `chain` vira arestas no parser
(`a→b→c`), e o motor de execução conhece **apenas DAG**. Um formato a mais no
arquivo, zero caminho a mais no runtime. `type: dag` com `depends_on` continua
disponível para fan-out/fan-in — os dois exemplos em `examples/` provam.

Escolha adicional: **sem `type`, assume `dag`**. Um arquivo sem `depends_on` vira
grafo de nós soltos, que rodam em paralelo. `chain` impõe ordem e por isso
precisa ser pedido explicitamente.

### 2. Nome do workflow

O YAML não tem `name`. Resolvido: o slug vem do nome do arquivo quando `name`
falta, e `name` tem precedência quando existe.

### 3. Validação

A §5 exige validar IDs duplicados, ciclos, dependências ausentes e configuração
inválida antes de salvar. Implementado, e o erro de ciclo **mostra o caminho**
(`a -> b -> c -> a`) em vez de só dizer que existe. Disponível sem banco via
`bravis validate`, para rodar no editor ou na CI.

---

## PRECISAM DE DECISÃO

### 4. `run:` viola a regra fundamental de execução — o gap mais profundo

A §3 diz:

> OUTRAS LINGUAGENS → KUBERNETES OBRIGATÓRIO
> O Bravis nunca deve tentar executar arbitrariamente código de outras linguagens
> diretamente dentro do processo principal.

E a §14:

> Não executar código arbitrário recebido pela API.
> Tasks locais devem ser compiladas e registradas no runtime.

O YAML tem `run: python fetch.py` e `run: ./notify.sh`, e o pedido é que **local
rode na própria instância**. São coisas incompatíveis como escritas.

O plano prevê *dois* executores: **Local Go** (tasks compiladas, registradas num
registry) e **Kubernetes** (todo o resto). O YAML quer um terceiro, que o plano
não tem: **um executor de processo**, que roda comando arbitrário no host.

**Opção A — manter a §3 e converter `run:` em pod.**
Prós: um só modelo de execução, mesma semântica local e em produção, nada de
código arbitrário no processo do orquestrador.
Contras: mata o caso de uso local (exige cluster ou Docker para um `echo`), e
contraria o pedido explícito de rodar na instância.

**Opção B — admitir um executor de processo, restrito ao modo local.**
Prós: atende ao pedido; desenvolvimento sem cluster; o `run:` funciona como
escrito.
Contras: abre execução arbitrária. Exige limites explícitos — diretório de
trabalho, usuário, timeout, variáveis de ambiente permitidas — e a §3 precisa ser
reescrita, não ignorada em silêncio.

**Recomendação: B, com a §3 emendada e o executor recusando-se a operar fora do
modo local.** O risco real não é o `run:` em si — é ele existir sem fronteira
declarada. Um `ProcessExecutor` que só aceita registro quando `BRAVIS_ENV=local`
torna a fronteira código, não convenção.

### 5. `action: docker.run` — um terceiro executor

O plano lista `DockerExecutor` como **futuro** (§13); o YAML o usa no exemplo
principal. E `volumes: ~/data:/work/data` é caminho de host — não existe em
Kubernetes.

Isso levanta uma pergunta que o plano não responde: **o mesmo arquivo deve rodar
local e em produção?** Se sim, `docker.run` com volume de host não é portável, e
`kubernetes.run` não roda local. As duas alternativas são um executor por
ambiente (arquivos diferentes por ambiente) ou uma ação abstrata `container.run`
que cada executor materializa como convier — com os volumes de host válidos
apenas local.

### 6. `action:` + `with:` — modelo de ações tipadas

O plano não tem esse conceito; é o modelo do GitHub Actions. Introduz um
registry de ações, validação de parâmetros por ação e versionamento delas.

Hoje o parser aceita `action` + `with` como dados livres e valida só a estrutura
(exatamente um entre `run` e `action`; `with` só com `action`). **A semântica de
cada ação — quais chaves, quais obrigatórias — ainda não existe.** Precisa de
decisão sobre quem registra ações e como os parâmetros são validados.

### 7. `schedule` inline contra entidade própria

A §22 tem `schedules` como tabela, sugerindo N agendas por workflow (ex.: um
cron diário e outro de reconciliação). O YAML tem um campo só.

Hoje o campo é guardado como string única. Se um workflow precisar de duas
agendas, o formato muda.

### 8. Sem `project`

A §4 define Project → Workflow → Run, e o schema tem `projects`. O YAML não
menciona projeto. Precisa definir: vem do diretório? De um `bravis.yaml` na
raiz? De um flag na publicação?

### 9. Campos operacionais ausentes

O plano exige retries (§7), timeout, concorrência (§9) e recursos (§13). O YAML
não os tem. Faltam por passo (`retries`, `timeout`) e por workflow
(`max_active_runs`, `concurrency`).

### 10. YAML contra banco como fonte da verdade

A §22 é explícita: *"Nunca depender exclusivamente do arquivo YAML após o
workflow ser publicado."* Falta desenhar o fluxo de publicação — arquivo →
validação → `workflow_version` imutável → Run tira snapshot. Sem isso, editar o
arquivo alteraria o significado de execuções passadas.

---

## Resumo

| # | gap | estado |
|---|---|---|
| 1 | `chain` vs DAG | resolvido (açúcar → arestas) |
| 2 | nome do workflow | resolvido (arquivo ou `name`) |
| 3 | validação | resolvido (`bravis validate`) |
| 4 | **`run:` vs §3/§14** | **decisão — recomendo executor de processo restrito ao local** |
| 5 | `docker.run` e portabilidade | decisão |
| 6 | ações tipadas | decisão |
| 7 | schedule único vs N | decisão |
| 8 | project ausente | decisão |
| 9 | retries/timeout/recursos | decisão |
| 10 | publicação e imutabilidade | desenho pendente (§22) |
