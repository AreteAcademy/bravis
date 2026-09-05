---
title: Parâmetros
description: O que muda entre dois disparos do mesmo workflow, sem editar o arquivo.
group: Referência
order: 8
slug: parameters
---

Um workflow pode declarar **parâmetros de execução**: valores que mudam entre
dois disparos sem que o arquivo mude.

## Declarando

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| campo | | |
|---|---|---|
| `name` | **obrigatório** | o nome usado no template e no `--param` |
| `type` | | `string` ou `boolean` |
| `default` | | usado quando o disparo não informa valor |
| `pattern` | | expressão regular que o valor precisa casar |

Um parâmetro **sem `default`** é obrigatório: o disparo que não o informar
falha antes de executar qualquer passo.

## Usando no comando

O valor entra no `run` por template, entre chaves duplas e com ponto:

```yaml
run: ./carregar.sh --desde {{ .start_date }}
```

## Passando na linha de comando

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` é repetível. Entrada sem `=` é **erro**:

```
erro: --param "load_full": use chave=valor
```

Isso é deliberado. `--param load_full`, esquecendo o valor, rodaria com o padrão
— e o operador acharia que o backfill aconteceu com o parâmetro que ele quis.

## No backfill

Os valores valem para **todos** os slots do intervalo. É o caso de uso central:
"reprocessa janeiro inteiro com `load_full=true`".

```bash
brevis backfill diario --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## Na interface

Um workflow com `params` ganha **formulário** no lugar do botão simples de
disparo. Os campos vêm da declaração, e o `pattern` é validado antes do envio.

## No ambiente do passo

Os parâmetros do run também entram no **ambiente** de cada passo, prefixados.
Isso existe para que um fetcher escrito com o [SDK](/docs/sdk/) os enxergue sem
receber nada por argumento:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## O snapshot

Os parâmetros são gravados **no run**, não lidos do workflow na hora de
executar. O run carrega a entrada com que foi disparado, e o log de uma execução
de janeiro continua mostrando os valores de janeiro mesmo depois de o padrão
mudar.

## Próximos passos

- [Configuração](/docs/configuration/) — variáveis de ambiente do processo
- [SDK](/docs/sdk/) — como um fetcher lê o contexto do run
