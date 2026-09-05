---
title: Quickstart
description: Do zero à DAG executando na interface, em quatro passos.
group: Começar
order: 3
slug: quickstart
---

Este guia sobe o brevis.sh, publica um workflow e o executa. Ao final você vê o
grafo com o estado de cada passo ao vivo.

Requisito: um PostgreSQL acessível e a variável `BREVIS_DATABASE_URL`.

## 1. Aplicar o schema

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Escrever um workflow

Crie `hello.yaml`. Este exemplo usa apenas shell, então roda em qualquer lugar:

```yaml
name: hello
type: dag
tags: [exemplo]

steps:
  - id: preparar
    run: sh -c 'echo preparando; sleep 1'

  # Os dois seguintes dependem do mesmo passo, então rodam EM PARALELO.
  - id: extrair
    run: sh -c 'sleep 2; echo 42 extraidos'
    depends_on: [preparar]

  - id: validar
    run: sh -c 'sleep 1; echo validado'
    depends_on: [preparar]

  - id: publicar
    run: echo publicado
    depends_on: [extrair, validar]
```

Confira antes de publicar — isto não toca no banco:

```bash
brevis validate hello.yaml
```

```
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

## 3. Executar agora, sem fila

O caminho mais curto para ver o grafo funcionando:

```bash
brevis run hello.yaml
```

```
workflow hello (dag, 4 steps) em .
  ▶ preparar
    preparar | preparando
  ✓ preparar
  ▶ extrair
  ▶ validar
    validar | validado
  ✓ validar
    extrair | 42 extraidos
  ✓ extrair
  ▶ publicar
    publicar | publicado
  ✓ publicar

workflow hello concluido
```

`extrair` e `validar` começam juntos porque dependem do mesmo passo. O runner
percorre o grafo por níveis: tudo dentro de um nível roda em paralelo, e o
nível seguinte só começa quando o anterior fecha inteiro.

:::note `run` é local por natureza
`brevis run` só opera com `BREVIS_ENV=local` (vazio conta como local) e executa
os passos como processos na própria instância — mesmo os que declaram `image:`.
Quem cria pods é o `scheduler`.
:::

## 4. Publicar e operar

Para que o workflow exista no banco, apareça na interface e siga a agenda:

```bash
brevis publish hello.yaml
```

```
  publicado  hello                    (manual)
```

Suba a interface e o scheduler em dois terminais:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Abra `http://localhost:8080`, encontre `hello` na lista de workflows e clique em
▶. O grafo mostra cada passo mudando de estado ao vivo.

## O que aconteceu

| | |
|---|---|
| `migrate up` | criou as tabelas |
| `validate` | conferiu o YAML sem tocar no banco |
| `run` | executou na própria instância, sem fila |
| `publish` | gravou o workflow e a agenda |
| `serve` | subiu API e interface |
| `scheduler` | materializou a agenda e executou a fila |

## Próximos passos

- [Workflows](/docs/workflows/) — o formato completo do YAML
- [Parâmetros](/docs/parameters/) — o que muda entre dois disparos
- [Pod por passo](/docs/pod-per-step/) — como isso vira Kubernetes
