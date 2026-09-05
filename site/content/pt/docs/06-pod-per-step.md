---
title: Pod por passo
description: Por que cada etapa executa em seu próprio pod, e o que isso muda em custo e isolamento.
group: Conceitos
order: 6
slug: pod-per-step
---

Em Kubernetes, **cada passo de um workflow vira um pod** com a imagem declarada
naquele passo. Não há worker genérico esperando trabalho: é o trabalho que traz
o seu runtime.

```
scheduler                          cluster
─────────                          ───────
percorre o grafo
  para cada passo pronto:
    monta o Pod  ──────────────►  Pod: imagem do passo, comando do passo
    acompanha o status  ◄───────  Pending → Running → Succeeded/Failed
    segue o log         ◄───────  stdout do container
    lê o exit code      ◄───────  containerStatuses[0].terminated
    apaga o pod  ──────────────►  (some)
```

## O que isso muda

Um worker monolítico obriga a imagem a conter tudo que qualquer passo possa
precisar. Na prática:

| | imagem única | pod por passo |
|---|---|---|
| passo de dbt | 1,9 GB · 1Gi de RAM | 1,9 GB · 1Gi de RAM |
| fetcher em Go ao lado | **1,9 GB · 1Gi** | **12 MB · 32Mi** |
| trocar a versão do dbt | rebuild de tudo | uma linha no YAML daquele workflow |
| um passo que vaza memória | derruba o worker e os vizinhos | morre sozinho |

O terceiro item é o menos óbvio e o mais caro no dia a dia: com imagem única,
subir o dbt de 1.10 para 1.11 num pipeline obriga a subir em todos.

## Imagens por papel

As imagens são por **papel**, não por projeto. Tudo que sobra numa imagem é
baixado por todo nó que rodar aquele passo:

| papel | tamanho | partida a frio |
|---|---|---|
| **Go** | **5,8 MB** | 0,18 s |
| **Python** | **118 MB** | 0,54 s |
| **dbt** | **620 MB** | 3,3 s |

Antes: uma imagem única de **1,87 GB** para tudo.

No caso do dbt, o `dbt parse` roda no build e o `target/partial_parse.msgpack`
viaja na imagem — **2,68 s a menos por pod**, em toda invocação.

## Onde cada passo roda

A decisão é tomada uma vez, no boot, e depende de `BREVIS_PODS` e de haver
cluster:

| `BREVIS_PODS` | com cluster | sem cluster |
|---|---|---|
| `auto` (padrão) | passo com `image:` vira pod | tudo em processo local, com aviso no log |
| `on` | passo com `image:` vira pod | **erro de boot** |
| `off` | tudo em processo local | tudo em processo local |

`auto` é o padrão porque o mesmo binário roda nos dois lugares: no laptop não há
service account montada e ele cai para processo local; no cluster há, e ele
passa a criar pods.

`on` existe para o deploy que **não pode** silenciosamente virar execução local.
Ali, ficar sem cluster tem de ser erro de boot.

:::warning `brevis run` não cria pods
O comando `run` monta apenas o executor de processo. Um passo com `image:`
executa na própria instância — e avisa. Silenciar faria parecer que o passo
rodou na imagem declarada, que é o tipo de engano que só aparece quando o
resultado já está errado.
:::

## Identidade e credenciais

Com que identidade os pods sobem é decisão da **instalação**, não do workflow —
um pipeline não deve poder escolher a service account com que roda. Por isso
vem do ambiente:

```bash
BREVIS_POD_NAMESPACE=dados
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

Em modo pod, o ambiente da task vem dos Secrets do cluster — não de
`BREVIS_TASK_ENV`.

## Node selector e tolerações

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=dados:NoSchedule'
```

Só o operador `Equal` é aceito nas tolerações: `Exists` toleraria **qualquer**
taint com aquela chave, amplo demais para uma decisão vinda de variável de
ambiente.

Uma toleração malformada é **ignorada** em vez de virar erro de boot — ela deixa
o pod `Pending`, que é visível, enquanto recusar o boot pararia também os
workflows que não precisam daquele pool.

## Depurando uma falha

```bash
BREVIS_POD_MANTER_EM_FALHA=true
```

Mantém o pod que falhou para inspeção com `kubectl logs` e `kubectl describe`.
Deixe desligado em produção: pods parados consomem cota do cluster.

## Próximos passos

- [Kubernetes](/docs/kubernetes/) — o deploy completo
- [Configuração](/docs/configuration/) — todas as variáveis `BREVIS_POD_*`
