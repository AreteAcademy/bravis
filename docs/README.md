# docs

**Atualizado em** 2026-09-04

---

## SDK

Comece por aqui. Estes cinco descrevem o SDK como ele está hoje
(`sdk/v0.21.0`) e respondem perguntas diferentes:

| documento | responde |
|---|---|
| [`SDK_ARQUITETURA.md`](SDK_ARQUITETURA.md) | **o quê e onde** — as quatro perguntas de um fetcher, o mapa dos pacotes, as duas interfaces, por onde um registro passa |
| [`SDK_NOVO_DRIVER.md`](SDK_NOVO_DRIVER.md) | **como** — o roteiro para acrescentar Postgres, MySQL e Redshift, com as oito regras e o checklist |
| [`SDK_MATRIZ.md`](SDK_MATRIZ.md) | **o que suporta o quê** — cada opção por driver, as combinações recusadas, e o que ainda não é verdade |
| [`SDK_CONSUMIDOR.md`](SDK_CONSUMIDOR.md) | **como cada defeito apareceu** — os onze achados pelo primeiro consumidor, e as seis classes que se repetiram |
| [`SDK_DECISOES.md`](SDK_DECISOES.md) | **por quê** — cada decisão, o que se tentou antes e o que aquilo custou |

A referência da API é o [godoc](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk)
e o [`sdk/README.md`](../sdk/README.md); o histórico versão a versão é o
[`CHANGELOG.md`](../CHANGELOG.md).

## Operação

| | |
|---|---|
| [`PUBLICAR.md`](PUBLICAR.md) | como publicar SDK e imagens |
| [`IMAGENS.md`](IMAGENS.md) | as imagens Docker |
| [`KUBERNETES.md`](KUBERNETES.md) | o deploy |
| [`PARAMS.md`](PARAMS.md) | parâmetros de execução do engine |

## Planos e specs

`plan/` guarda as specs, em ordem cronológica. Cada uma foi escrita antes da
mudança e executada depois, então serve como registro do que se pediu e do que
de fato saiu — **não** como descrição do estado atual.

| spec | virou |
|---|---|
| [`2026-09-03-sdk-recebe-contexto-do-engine.md`](plan/2026-09-03-sdk-recebe-contexto-do-engine.md) | `v0.10.0` |
| [`2026-09-03-sdk-conserto-do-merge.md`](plan/2026-09-03-sdk-conserto-do-merge.md) | `v0.12.0` |
| [`2026-09-03-sdk-schema-declarado.md`](plan/2026-09-03-sdk-schema-declarado.md) | **parcial** — veja §13 de `SDK_DECISOES.md` |
| [`2026-09-03-sdk-validacao-do-consumidor.md`](plan/2026-09-03-sdk-validacao-do-consumidor.md) | `v0.17.0` |
| [`2026-09-04-sdk-uma-declaracao-de-colunas.md`](plan/2026-09-04-sdk-uma-declaracao-de-colunas.md) | `v0.18.0` |
| [`2026-09-04-sdk-drivers-mvp.md`](plan/2026-09-04-sdk-drivers-mvp.md) | fase 0 = `v0.19.0`, fase 1 = `v0.20.0`; **fases 2–5 em aberto** |

`phases/` é do **engine**, não do SDK: as fases de construção do orquestrador.

## Histórico — não são o estado atual

Estes descrevem versões que já não existem. Ficam pelo registro; nada aqui deve
ser lido como a API de hoje.

| | descreve |
|---|---|
| [`SDK.md`](SDK.md) | o prompt original de construção do SDK, de 2026-09-02 |
| [`SDK_V2.md`](SDK_V2.md) | a evolução pedida para a `v0.2` |
| [`SDK_LOAD.md`](SDK_LOAD.md) | o conserto do load da `v0.2.1` |
| [`SDK_V9.md`](SDK_V9.md) | relatório do consumidor sobre a `v0.9.x` |
| [`plan.md`](plan.md) | o plano original do engine |
| [`gaps-yaml-vs-plano.md`](gaps-yaml-vs-plano.md) | levantamento de agosto |
