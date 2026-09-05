# Execução em Kubernetes — um pod por passo

## A dinâmica

```
scheduler                          cluster
─────────                          ───────
lê a agenda
cria o Run  ──────────────────►  (nada ainda)
percorre o grafo
  para cada passo pronto:
    monta o Pod  ──────────────►  Pod: imagem do passo, comando do passo
    acompanha o status  ◄───────  Pending → Running → Succeeded/Failed
    segue o log         ◄───────  stdout do container
    lê o exit code      ◄───────  containerStatuses[0].terminated
    apaga o pod  ──────────────►  (some)
```

O pod sobe com a **imagem exata daquele passo** e um comando. Não há worker
genérico esperando trabalho: o trabalho é que traz o seu runtime.

## Por que isso é o ponto do projeto

Um worker monolítico obriga a imagem a conter tudo que qualquer passo possa
precisar. Na prática:

| | imagem única | pod por passo |
|---|---|---|
| passo de dbt | 1,9 GB, 1Gi de RAM | 1,9 GB, 1Gi |
| fetcher em Go ao lado | **1,9 GB, 1Gi** | **12 MB, 32Mi** |
| trocar a versão do dbt | rebuild de tudo | uma linha no YAML daquele workflow |
| um passo que vaza memória | derruba o worker e os vizinhos | morre sozinho |

O terceiro item é o menos óbvio e o mais caro no dia a dia: com imagem única,
subir o dbt de 1.10 para 1.11 num pipeline obriga a subir em todos.

## O YAML

```yaml
name: platform_workspace
schedule: "0 5 * * *"

image: us-central1-docker.pkg.dev/zarv/apps/dbt:1.10.3   # padrão dos passos
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}

steps:
  - id: bronze_workspace
    run: dbt build --select bronze_workspace+

  - id: notificar
    image: ghcr.io/zarvhq/notify:0.3   # binário Go: outro runtime, outro tamanho
    shell: false                       # distroless não tem shell
    run: /notify --canal dados
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze_workspace]
```

`image` e `resources` no topo são o padrão; o passo sobrescreve o que precisar,
campo a campo — um passo pode pedir só mais memória sem perder a CPU do padrão.

`shell: false` passa o comando como argv. Existe para imagem distroless, onde
`/bin/sh` não existe e `sh -c` falharia com "no such file or directory" — erro
correto e que não diz nada sobre a causa.

## O mesmo YAML roda local

`BREVIS_PODS=auto` (padrão): sem service account montada, o passo roda como
processo na própria instância e o `image:` é **ignorado com aviso no log** —
silenciar faria parecer que rodou na imagem declarada.

No deploy do cluster use `BREVIS_PODS=on`: ali, ficar sem cluster tem de ser erro
de boot. Com `auto`, uma falha de montagem da service account faria o scheduler
executar tudo dentro do próprio pod de 128Mi, em silêncio.

## O que o scheduler faz (e o que não faz)

Ele **não** executa dbt nem Python. Ele fala HTTP com o servidor de API e SQL com
o Postgres — por isso o Deployment pede 50m de CPU e 128Mi. O trabalho pesado
está nos pods que ele cria.

Quatro chamadas REST, escritas sobre a stdlib: criar pod, ler status, ler log,
apagar pod. Sem `client-go`: a biblioteca oficial traz centenas de dependências e
dezenas de MB para isto — a mesma conta que levou a vendorizar o React em vez de
adotar npm.

## Decisões que a implementação fixa

- **`restartPolicy: Never`.** Quem conta tentativas e aplica backoff é o
  dispatcher. Deixar o kubelet reiniciar criaria uma segunda política de retry,
  invisível para o histórico.
- **Nome determinístico** por (run, passo, tentativa). Se o processo morrer entre
  criar o pod e registrar isso, a tentativa seguinte adota o pod existente em vez
  de subir um segundo rodando o mesmo dbt em paralelo.
- **`activeDeadlineSeconds`** espelha o timeout. É a rede de segurança do lado do
  cluster: se o Brevis morrer, o pod ainda para sozinho.
- **Motivo da espera vira log.** `ImagePullBackOff` e `CreateContainerConfigError`
  não produzem saída nenhuma; sem reportá-los, o passo pareceria travado até o
  timeout, sem uma linha explicando.
- **O log é drenado depois do fim**, além de seguido ao vivo. Fechar o canal com
  linhas no buffer perderia justamente as últimas — as que explicam a falha.
- **Pod de sucesso é apagado; o que falha pode ficar** (`BREVIS_POD_MANTER_EM_FALHA`).
  Milhares de pods `Completed` poluem o namespace e não dizem nada que o
  histórico do Brevis não diga melhor.
- **O `reason` do cluster entra na mensagem de falha.** `OOMKilled` e
  `DeadlineExceeded` pedem ações opostas de "o código falhou".

## Variáveis dentro da task

A task não herda o ambiente do orquestrador — ele carrega `BREVIS_DATABASE_URL`
com credencial, e um workflow é um comando arbitrário escrito por outra pessoa.

Há dois caminhos, e a diferença entre eles é **quem escolhe**.

### Da instalação, para toda task

| modo | mecanismo |
|---|---|
| pod | `BREVIS_POD_ENV_FROM_SECRETS=meu-secret` → vira `envFrom.secretRef` no pod. As variáveis vão do Secret direto para a task, **sem passar pelo scheduler**. |
| local | `BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE` → repassa essas do ambiente do processo. `NOME=valor` define literal; `*` repassa tudo menos as `BREVIS_*`. |

Em ambos, `PATH` e `HOME` entram sempre: sem `PATH` nenhum comando resolve, e o
erro seria um "not found" que não explica nada.

O alcance é **todo passo de todo workflow**. Para uma credencial que só um
fetcher usa, isso é mais alcance que necessidade: o cookie de um vendor entra
também no pod do `dbt build` ao lado.

### Do YAML, por passo

```yaml
steps:
  - id: fetch_occurrences
    run: /usr/local/bin/gabriel
    env:
      BREVIS_LOG_LEVEL: info              # literal — o arquivo está no git
    secrets:
      GABRIEL_SESSION_COOKIE: gabriel-session/cookie
```

São duas chaves e não uma de propósito. Com uma só, o caminho mais curto para
fazer funcionar seria colar o segredo no YAML.

| | `env:` | `secrets:` |
|---|---|---|
| o valor está no arquivo | sim | **não**, só a coordenada |
| em pod | `env: [{name, value}]` | `valueFrom.secretKeyRef` — quem lê é o kubelet |
| em local | literal | a variável de mesmo nome no ambiente do motor; **ausente é erro** |

Os dois herdam do workflow para o passo, nome a nome, como `image` e
`resources`. Precedência, do mais fraco ao mais forte: ambiente global do motor,
`env:` do workflow, `env:` do passo.

### E a instalação continua decidendo quais segredos existem

`secrets:` inverte quem escolhe, e o YAML é escrito por outra pessoa. Sem
limite, um workflow montaria qualquer Secret do namespace — inclusive o do banco
do próprio Brevis — e rodaria um comando arbitrário com ele em mãos.

```bash
BREVIS_POD_ALLOWED_SECRETS=gabriel-session,ana-api
```

**Vazia nega tudo.** Negar por padrão custa uma variável na instalação; permitir
por padrão custa o inverso, e o inverso é irreversível. A recusa acontece na
montagem do pod, com o nome do Secret e onde liberá-lo — não no servidor, que
aceitaria o `secretKeyRef` e falharia depois por outro motivo.

A divisão final: **a instalação diz quais segredos existem para workflows, o
YAML diz qual passo recebe cada um.**

## O volume da credencial

Uma credencial que **rotaciona** — um cookie de sessão com janela deslizante —
morre com o pod se o valor novo não for a lugar nenhum. Sem isso, alguém recola
a semente por janela, para sempre.

Com um volume, a troca é outra: a variável de ambiente deixa de guardar o valor
**rotativo** e passa a guardar uma chave **estática**. Cola-se uma vez.

```bash
BREVIS_POD_CREDENTIAL_PVC=brevis-credentials
BREVIS_POD_CREDENTIAL_PATH=/var/brevis/credentials   # opcional, é o padrão
```

Com o PVC definido, **todo pod de passo** ganha o volume e a env
`BREVIS_CREDENTIAL_DIR` apontando para o mount — que é a mesma variável que o
SDK lê quando alguém roda na própria máquina com `BREVIS_CREDENTIAL_DIR=./.brevis`.
O mesmo código nos dois. Sem o PVC, nada muda.

Um passo que declare o próprio `BREVIS_CREDENTIAL_DIR` no `env:` vence a
injeção, e a variável não vai duplicada.

### O conteúdo é cifrado, e o motor não tem a chave

O SDK grava AES-256-GCM com a chave de `BREVIS_CREDENTIAL_KEY`, que é um Secret
comum e entra por `BREVIS_POD_ENV_FROM_SECRETS` ou por `secrets:` no YAML. **Sem
chave o SDK recusa a ligar o store** em vez de gravar em claro — um volume vira
snapshot, snapshot vira backup, e backup vira um lugar onde ninguém lembra que
há credencial.

```bash
head -c 32 /dev/urandom | base64    # a chave, uma vez
```

### O PV, para GCS Fuse

O cluster de dev é GKE, e `ReadWriteMany` ali não é EFS. Das três opções, a que
serve é o **GCS Fuse CSI**: RWX de verdade, e o custo de guardar alguns KB é de
centavos. `Filestore` tem instância mínima de 1 TiB; um Persistent Disk RWO não
compartilha entre nós.

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: brevis-credentials
spec:
  accessModes: [ReadWriteMany]
  capacity: {storage: 1Gi}      # ignorado pelo gcsfuse; o campo é obrigatório
  storageClassName: ""
  # O SDK recusa diretório com permissão frouxa, então o modo vem do mount:
  # um volume compartilhado a 0755 é legível por todo pod que o monta.
  mountOptions:
    - implicit-dirs
    - uid=0
    - gid=0
    - dir-mode=0700
    - file-mode=0600
  csi:
    driver: gcsfuse.csi.storage.gke.io
    volumeHandle: SEU-BUCKET
```

Confirme antes que o driver está habilitado (`gcsFuseCsiDriver` no addon config)
e que a service account dos pods tem `roles/storage.objectAdmin` no bucket.

**Uma ressalva honesta:** `rename` no gcsfuse **não é atômico** como num POSIX de
verdade. O SDK grava em temporário e renomeia, o que cobre a queda no meio da
escrita num sistema de arquivos normal; no gcsfuse, para um arquivo de poucos KB
escrito por um pod de cada vez, o risco é baixo — mas é real, e é mais um motivo
para manter `concurrency: 1` no workflow que usa isso.

O SDK grava **último a escrever vence**, e isso é escolha: no fornecedor que
motivou a feature, rotacionar não invalida o token anterior, então dois valores
concorrentes ambos funcionam. Para um fornecedor que invalide o anterior, não
use sem uma trava sua.

## Segurança

Duas contas, e é a separação que importa:

| conta | permissão |
|---|---|
| `brevis-scheduler` | criar, ler, listar, observar e apagar pods; ler logs. Sem `update`, sem `patch`. |
| `brevis-task` | **nenhuma** |

O pod de task executa comandos vindos de um YAML. Se herdasse a conta do
scheduler, qualquer workflow poderia criar pods, ler secrets e escalar sozinho.
Com uma conta sem role, o pior que um comando arbitrário faz é usar as
credenciais que a instalação deu explicitamente a ele — via
`BREVIS_POD_ENV_FROM_SECRETS`, que vem do ambiente do scheduler, ou via um
`secrets:` do YAML **que só alcança os Secrets em `BREVIS_POD_ALLOWED_SECRETS`**.

O YAML nunca amplia o conjunto; ele só escolhe, dentro do que a instalação
liberou, qual passo recebe o quê.

## Concorrência: três limites

| onde | conta | protege |
|---|---|---|
| `--concurrency` | RUNS simultâneos no dispatcher | o processo |
| `--max-pods` | PASSOS simultâneos = pods vivos | o **cluster** |
| `concurrency:` no YAML | runs simultâneos **do mesmo workflow** | o **dado** |

O terceiro é o que impede um `*/15` que leva 20 minutos de se sobrepor a si
mesmo — dois `dbt build` no mesmo modelo disputando a mesma tabela. Trinta e seis
dos 51 flows do repositório de dados declaravam esse limite no Kestra.

```yaml
name: id_verification_today
schedule: "5-59/15 * * * *"
concurrency: 1
```

Ele é imposto **na própria consulta de claim**, pelo mesmo motivo que o limite
global: não existe caminho em que mais itens saiam da fila do que o permitido.
Reivindicar e depois devolver seria uma janela em que dois dispatchers já teriam
pegado o mesmo workflow.

Dois detalhes que a implementação fixa:

- **Conta itens reivindicados, não runs em `running`.** Entre o claim e a
  transição de estado há um instante em que o run ainda está `queued`; contar por
  status abriria exatamente essa fresta.
- **Numeração por workflow dentro do lote.** Sem ela, três itens do mesmo
  workflow com limite 1 sairiam todos no mesmo claim — a contagem não muda no
  meio da consulta.

Um workflow no limite **não bloqueia os outros**: a fila é compartilhada, e
travar tudo por causa de um seria pior que não ter limite.

Só o primeiro não basta: cinco runs com três passos paralelos cada dariam
**quinze** pods. O `--max-pods` é um semáforo compartilhado por todos os runs do
processo — com dez passos prontos e cinco vagas, cinco correm e os demais entram
conforme as vagas se abrem.

Medido, com dez passos sem dependência entre si e `--max-pods 5`:

```
passos | pico_simultaneo | duracao_total
    10 |               5 | 00:00:12
```

Dois lotes de seis segundos. O pico bateu exatamente no teto — nem seis (que
seria vazamento) nem menos (que seria o limite virando serialização).

A vaga é tomada **por tentativa**, não pelo passo inteiro: segurar o lugar
durante o backoff de um retry deixaria uma vaga do cluster ociosa esperando um
relógio.

O semáforo vive no processo, e o Deployment tem uma réplica com `Recreate` por
isso. Com duas réplicas, cada uma teria o próprio teto — quando houver eleição de
líder ou contagem no banco, o limite passa a ser global de verdade.

## Vindo do Leoflow

O que lá era um arquivo de empacotamento por DAG (`schema_version`, `dag_id`,
`base_image`, `build.platforms`, `tasks.<id>.execution`) aqui se divide em dois
lugares, por uma razão: **o que é do pipeline fica no YAML; o que é da
instalação fica no ambiente do scheduler.**

| Leoflow (por DAG) | Brevis | onde |
|---|---|---|
| `dag_id`, `description`, `tags` | `name`, `tags` | workflow |
| `base_image` | `image:` | workflow (por passo, com padrão) |
| `tasks.run.resources` | `resources:` | workflow (por passo) |
| `variables: [...]` | `BREVIS_POD_ENV_FROM_SECRETS` | instalação |
| `execution.service_account` | `BREVIS_POD_SERVICE_ACCOUNT` | instalação |
| `execution.node_selector` | `BREVIS_POD_NODE_SELECTOR` | instalação |
| `execution.tolerations` | `BREVIS_POD_TOLERATIONS` | instalação |
| `build.platforms` | — | a imagem é construída fora, uma vez |
| `params.get('x')` (Airflow) | `params:` + `{{ .x }}` | workflow |
| `alerts.on_failure` | `BREVIS_SLACK_WEBHOOK` | instalação |
| `connections` | **não existe** | — |

Service account no YAML do pipeline seria a inversão perigosa: um arquivo de
workflow escolhendo com que identidade roda no cluster. Por isso essas três
ficam no ambiente do scheduler.

**Não há registro nem build por DAG.** No Leoflow, publicar um DAG novo passava
por `leoflow deploy`; aqui a pasta é o artefato — um ConfigMap com os YAMLs e um
Job que roda `brevis publish --prune`. Ver `publish-job.yaml`.

**A ausência que sobra é *connection*** — não há conexão nomeada. O alerta de
falha existe (ver abaixo).

## Alerta de falha

```bash
kubectl -n dados create secret generic brevis-slack \
  --from-literal=webhook='https://hooks.slack.com/services/...'
```

Configurado o webhook, **todo workflow passa a avisar** — sem repetir bloco
nenhum no YAML. Era a diferença de desenho frente ao Kestra: lá, os 51 flows
carregavam cada um o mesmo `errors: alert_slack` copiado, vinte linhas de payload
cinquenta vezes.

Três decisões:

- **O alerta sai quando o run DESISTE**, não a cada tentativa. Avisar em toda
  falha transformaria um retry bem-sucedido em dois alertas e um silêncio, e
  canal que grita à toa deixa de ser lido.
- **O webhook nunca vem do YAML.** É credencial: quem tem a URL posta no canal
  como se fosse a plataforma.
- **Falhar ao avisar não derruba nada.** Webhook fora do ar vira log; o run
  termina em FAILED e a fila continua sendo consumida.

A mensagem traz domínio e pipeline (das `tags`, com o prefixo do slug como
reserva), origem, tentativas, data lógica, as últimas linhas do erro e o link
direto para a execução. O erro é truncado em 900 caracteres — o bloco do Slack
recusa a mensagem *inteira* acima de 3000, então truncar é o que garante que o
alerta chegue.

## Aplicar

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl -n dados create secret generic brevis-db --from-literal=url='postgres://...'
kubectl -n dados create secret generic brevis-task-env \
  --from-literal=STAGE=prod --from-literal=GOOGLE_PROJECT_ID=zarv-...
kubectl -n dados create configmap brevis-brand --from-file=brand.yaml
kubectl apply -f deployments/kubernetes/api.yaml -f deployments/kubernetes/scheduler.yaml
```

`job-exemplo.yaml` mostra, escrito à mão, o pod que o scheduler monta — útil para
conferir o que o cluster vai receber antes de qualquer coisa rodar.

## O que ainda não existe

- **Volumes.** Nenhum passo monta PVC ou emptyDir; o que precisa passar entre
  passos vai pelo warehouse. `volumes:` no YAML é o próximo passo natural.
- **Eleição de líder.** Uma réplica do scheduler, e é por isso que o Deployment
  usa `Recreate`.
- **Job/CronJob nativo.** Os pods são criados diretamente. Um `Job` traria retry
  do lado do cluster, que é justamente a segunda política de retry que se evitou.
- **Sidecars e initContainers.**
