---
title: Configuração
description: Todas as variáveis de ambiente, com padrão, formato e o que acontece quando faltam.
group: Referência
order: 9
slug: configuration
---

Toda a configuração vem do **ambiente**, lida uma vez no boot. Nada consulta o
ambiente depois: um processo cuja configuração muda no meio da execução é um
processo cujo comportamento ninguém consegue reproduzir.

## Obrigatória

| variável | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Ausente = **erro de boot** nos cinco subcomandos que usam banco |

## Processo

| variável | padrão | |
|---|---|---|
| `BREVIS_ENV` | `local` | `local` usa log em texto e libera a interface sem senha; o resto exige credencial e loga JSON |
| `BREVIS_HTTP_ADDR` | `:8080` | endereço de escuta |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | inteiro; valor não numérico é erro de boot |
| `BREVIS_BRAND_FILE` | `brand.yaml` | identidade visual; ausente = padrão |
| `BREVIS_UI_URL` | — | base do link da execução no alerta |
| `BREVIS_SLACK_WEBHOOK` | — | destino do alerta de falha. Vazio = ninguém é avisado |

## Autenticação

| variável | |
|---|---|
| `BREVIS_AUTH_USUARIO` | usuário da interface |
| `BREVIS_AUTH_SENHA_HASH` | hash `pbkdf2-sha256$…`, gerado por `brevis hash` |
| `BREVIS_AUTH_SEGREDO` | 32+ bytes para assinar a sessão (`openssl rand -base64 48`) |

As três vêm juntas ou nenhuma vem.

:::danger Metade configurada é erro de boot
Quem preencheu o usuário acredita que fechou a porta. A interface dispara
pipeline: um `POST /workflows/<slug>/trigger` roda um `dbt build` que escreve no
warehouse. Aberta na internet, ela é um controle remoto do warehouse — e um
aviso no log não desfaz essa crença, porque ninguém lê o log de um processo que
funciona.
:::

Fora de `BREVIS_ENV=local`, subir sem credencial é recusado:

```
erro: BREVIS_ENV=production exige credencial: defina BREVIS_AUTH_USUARIO,
BREVIS_AUTH_SENHA_HASH (gere com `brevis hash`) e BREVIS_AUTH_SEGREDO
```

## Ambiente das tasks

| variável | padrão | |
|---|---|---|
| `BREVIS_TASK_ENV` | vazio | o que cada passo recebe |

A task **não herda** o ambiente do orquestrador. A razão é concreta: o processo
carrega `BREVIS_DATABASE_URL` com usuário e senha do Postgres, e um workflow é
um comando arbitrário escrito por outra pessoa — herdar entregaria a credencial
do banco a todo passo de todo pipeline.

O que a task precisa é declarado:

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # repassa essas duas do processo
BREVIS_TASK_ENV=STAGE=prod                # define um literal
BREVIS_TASK_ENV='*'                       # repassa tudo MENOS as BREVIS_*
```

`PATH` e `HOME` entram sempre — sem `PATH` nenhum comando resolve, e o erro
seria um "not found" que não explica nada.

Um nome ausente **não** vira string vazia: `GOOGLE_PROJECT_ID=""` faria o dbt
falhar com uma mensagem pior que a de variável ausente.

## Kubernetes

Decisões da **instalação**, não do workflow.

| variável | padrão | formato |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on` ou `off`; inválido é erro de boot |
| `BREVIS_POD_NAMESPACE` | o do cluster | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | lista por vírgula |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | lista por vírgula |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | lista por vírgula |
| `BREVIS_POD_ALLOWED_SECRETS` | — | lista por vírgula |
| `BREVIS_POD_CREDENTIAL_PATH` | — | caminho montado no pod |
| `BREVIS_POD_CREDENTIAL_PVC` | — | PVC com as credenciais |
| `BREVIS_POD_NODE_SELECTOR` | — | `chave=valor,outra=valor` |
| `BREVIS_POD_TOLERATIONS` | — | `chave=valor:efeito,…`; só o operador `Equal` |
| `BREVIS_POD_MANTER_EM_FALHA` | `false` | `true` mantém o pod para inspeção |

Em listas, vazios são ignorados: `a,,b` é erro de digitação, e um nome de secret
vazio faria o servidor recusar o pod inteiro.

## Testes

| variável | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | banco dos testes de integração; sem ela, eles pulam |

## Endpoints de saúde

| | |
|---|---|
| `GET /health` | liveness — **não** consulta o banco |
| `GET /ready` | readiness — consulta, e nomeia a dependência que falhou |

A separação é deliberada: liveness que depende de serviço externo faz o
Kubernetes **matar o pod** quando o banco oscila, em vez de apenas tirá-lo do
balanceador.

## Exemplo completo

```bash
# obrigatória
export BREVIS_DATABASE_URL='postgres://brevis:senha@db:5432/brevis?sslmode=require'

# produção exige credencial
export BREVIS_ENV=production
export BREVIS_AUTH_USUARIO=operador
export BREVIS_AUTH_SENHA_HASH="$(brevis hash < senha.txt)"
export BREVIS_AUTH_SEGREDO="$(openssl rand -base64 48)"

# o que as tasks precisam, e nada além
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=dados
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alertas
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.exemplo.com'
```
