---
title: Introdução
description: O que é o brevis.sh, que problema ele resolve e quando ele não é a escolha certa.
group: Começar
order: 1
slug: introduction
---

O brevis.sh é um **runtime de orquestração de dados escrito em Go**. Um binário
único reúne o que normalmente exige quatro ferramentas: transformação
declarativa, orquestração de workflows, fila persistente, scheduler e uma
interface para operar tudo isso.

Ele é open source sob licença MIT, e o projeto nasce na
[Aretê Academy](https://areteacademy.com.br/).

## O problema

Uma stack de dados comum reúne uma ferramenta de transformação, uma de
orquestração, uma fila e uma interface. Cada peça é boa no que faz — e a
integração entre elas vira trabalho permanente: quatro versionamentos, quatro
formas de configurar, quatro lugares para olhar quando algo falha.

Esse custo não aparece num diagrama de arquitetura. Aparece no tempo que a
equipe gasta mantendo a cola entre as peças em vez de trabalhar no domínio que
só ela conhece.

## A proposta

Um artefato, com o subcomando que o papel exige:

```bash
brevis serve        # API + interface
brevis scheduler    # materializa agendas e executa a fila
brevis run wf.yaml  # executa agora, sem banco
```

O workflow é um arquivo YAML versionado. Revisar um pipeline vira revisar um
diff, não uma sessão de cliques.

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.exemplo/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notificar
    image: ghcr.io/exemplo/notify:0.3
    run: /notify --canal dados
    depends_on: [bronze]
```

## O que o torna diferente

**Cada passo executa em seu próprio pod, com sua própria imagem.** Não existe
worker genérico esperando trabalho: é o trabalho que traz o seu runtime. Um
passo de `dbt` sobe com a imagem de dbt; um fetcher em Go ao lado sobe com 5,8
MB e 32Mi de memória, em vez de herdar o tamanho do maior vizinho.

**O scheduler cria os runs; a fila os executa.** São dois laços independentes,
e essa separação é deliberada: um pode cair sem interromper o outro, e
reprocessar um mês passado não depende do relógio.

**A validação não precisa de banco.** `brevis validate` roda na CI junto com os
testes, então um erro de YAML falha no pull request e não no cluster.

## Quando o brevis.sh não é a escolha

Documentar o que uma ferramenta não faz poupa mais tempo do que documentar o
que ela faz:

- **Você já opera Airflow ou Dagster e está satisfeito.** A migração custa mais
  do que a economia de operar um binário a menos.
- **Seus workflows não são de dados.** O brevis.sh assume DAGs de transformação
  e carga; um orquestrador de propósito geral serve melhor.
- **Você precisa de um catálogo de operadores prontos.** Aqui um passo é uma
  imagem e um comando. Isso é simples e é limitado — não há centenas de
  integrações prontas para arrastar.
- **Sua equipe não usa Kubernetes nem pretende usar.** O modo local funciona,
  mas o desenho supõe pods.

## Por onde seguir

| se você quer | vá para |
|---|---|
| instalar e ver rodando | [Instalação](/docs/installation/) e [Quickstart](/docs/quickstart/) |
| entender o formato do workflow | [Workflows](/docs/workflows/) |
| entender a arquitetura | [Scheduler e fila](/docs/scheduler-and-queue/) |
| a lista de comandos e flags | [CLI](/docs/cli/) |
| escrever um fetcher em Go | [SDK](/docs/sdk/) |

:::note O nome
*Brevis* é latim para "curto, breve" — a raiz de *brevidade*. Vem de
**De Brevitate Vitae**, de Sêneca: não é que temos pouco tempo, é que perdemos
muito dele.
:::
