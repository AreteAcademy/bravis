# Parâmetros de execução

O que muda entre dois disparos do mesmo workflow sem editar o arquivo.

```yaml
name: id_verification
schedule: "0 4 * * *"

params:
  - name: load_full
    type: boolean
    default: "false"
  - name: full_refresh
    type: boolean
    default: "false"

steps:
  - id: run
    run: >-
      dbt build --vars '{"load_full":"{{ .load_full }}"}'
      {{ if eq .full_refresh "true" }} --full-refresh{{ end }}
      --select bronze_id_verification+
```

| onde | como |
|---|---|
| CLI local | `brevis run wf.yaml --param load_full=true` |
| Backfill | `brevis backfill diario --from … --to … --param load_full=true` |
| UI | formulário na página do workflow (aparece só se houver params) |
| Cron | sempre os **padrões** — não há quem informe valores às 4 da manhã |

Os valores usados ficam gravados na coluna `runs.params`. "Com que parâmetros
isso rodou?" é a primeira pergunta de qualquer investigação de backfill, e a
resposta não pode depender de log.

## Tipos

`boolean`, `integer` e `string`. Sem `type`, é `string` — o tipo mais comum e o
único que não muda o significado do valor.

`enum` restringe a uma lista; `pattern` a uma expressão regular. Ambos são
validados no servidor, e o formulário da UI escolhe o controle a partir do tipo
(select para boolean e enum, number para integer).

## Injeção de shell

O valor de um param vai **para dentro da linha de comando** do passo, e quem
dispara um run não é necessariamente quem escreveu o workflow. Por isso um
`string` sem `pattern` aceita apenas:

```
letras  dígitos  _ . : / = , + @ -  espaço
```

Fora ficam aspas, `;`, `|`, `&`, `$`, crase, parênteses e redirecionamentos —
tudo que o shell interpreta. `--date {{ .data }}` com `data = "; rm -rf /"` é
recusado antes de o run existir.

O conjunto cobre o que os params reais deste repositório precisam: datas,
selectors do dbt, uids, caminhos, listas separadas por vírgula. Quem precisa
mesmo de um caractere fora dele declara `pattern:` — e aí a decisão é explícita,
do autor do workflow.

## Erros que o desenho evita

- **Nome desconhecido é erro**, não silêncio: `--param lod_full=true` com typo
  rodaria com o padrão e ninguém perceberia que o backfill não aconteceu.
- **Template com nome errado falha na montagem da task**, citando os params
  disponíveis. Sem `missingkey=error`, `{{ .lod_full }}` viraria string vazia e
  o comando sairia silenciosamente errado — `--select ` sem alvo.
- **Default inválido falha na publicação.** Um default recusado só apareceria no
  primeiro disparo agendado, de madrugada.

## `image:` não é templatável

De propósito. Quem dispara um run escolheria a imagem que o pod roda — ou seja,
o código que executa. Só o comando é renderizado.

## Vindo do Kestra

`brevis/bin/from-kestra.py` no repositório de dados traduz `inputs:` para
`params:` e `{{ inputs.x }}` para `{{ .x }}`, incluindo os condicionais
(`x == true ? '--flag' : ''` vira `{{ if eq .x "true" }}`). Isso destravou 6 dos
10 flows que antes não convertiam.

Ficam de fora: `{% if %}` do Jinja, funções (`now() | dateAdd(...)`) e
expressões compostas. Nesses casos o conversor **aborta o arquivo** em vez de
gerar um comando que só falha em execução.
