# Imagens por papel

Três imagens, cada uma com o mínimo do seu papel. Em Kubernetes cada passo é um
pod, então tudo que sobra na imagem é baixado por todo nó que rodar aquele passo.

| papel | tamanho | partida a frio | onde |
|---|---|---|---|
| **Go** | **5,8 MB** | 0,18 s | `imagens/Dockerfile.go` |
| **Python** | **118 MB** | 0,54 s | `imagens/Dockerfile.python` |
| **dbt** | **620 MB** | 3,3 s | `imagens/Dockerfile.dbt` |

Antes: uma imagem única de **1,87 GB** para tudo. Um fetcher em Go ao lado de um
`dbt build` pagava 1,87 GB e o pico de memória do maior — hoje paga 5,8 MB e
32Mi.

## dbt

### `dbt parse` no build economiza 2,68 s por pod

O que **não** dá para pré-executar é `dbt build`: ele roda SQL no BigQuery, e um
build de imagem que materializa tabela é um efeito colateral inaceitável.

O que dá é o **parse**. `dbt parse` gera `target/partial_parse.msgpack`, e o
resultado viaja na imagem:

```
parse frio (sem target/)              6,56 s
parse quente (cache da imagem)        3,88 s   → 41% menos
```

Depois disso, `dbt parse` custa praticamente o mesmo que `dbt --version` (3,50 s
contra 3,42 s): o que sobra é o tempo de importar o próprio dbt, que nenhuma
imagem elimina.

### O parse precisa das MESMAS variáveis do runtime

Medido: o dbt registra quais `env_var` o profile usou e **invalida o cache**
quando elas mudam.

```
parse com GOOGLE_PROJECT_ID=a, execução com a    3,81 s
parse com GOOGLE_PROJECT_ID=a, execução com b    6,30 s   ← cache descartado
```

Por isso o `Dockerfile.dbt` **exige** `--build-arg GOOGLE_PROJECT_ID` e falha o
build sem ele. `STAGE` e `DBT_KEYFILE` (o caminho, não a chave) seguem a mesma
regra.

Consequência aceita: **uma imagem por ambiente**. O artefato fica acoplado ao
projeto do BigQuery, mas 2,68 s por pod em toda invocação paga isso.

### Sem pandas, pyarrow e numpy

290 MB que o `dbt-bigquery` arrasta e este projeto não exercita. Verificado com
`dbt build` de verdade — dois modelos incrementais e cinco testes contra o
BigQuery, `PASS=11`.

Voltam a ser necessários se o projeto adotar **modelos Python do dbt**. Até lá
são download e disco em todo nó do cluster.

### O que ficou de fora, e por quê

- `gcloud` CLI e `build-essential`: eram da imagem única, não do dbt.
- `git`: só existe no estágio de build, para o `dbt deps`. A imagem final não vai
  à rede no boot.
- `tini`: o dbt é PID 1 e termina sozinho.
- Os **seeds ficam** (102 MB). O dbt calcula o checksum deles no parse; sem eles
  o parse falha. Estão numa camada própria, antes dos modelos, porque mudam por
  trimestre enquanto o SQL muda por semana.

## Python

Base **distroless** em vez de `python:3.11-slim`: o slim traz apt, dpkg, bash e
um sistema de arquivos inteiro que o pod nunca usa.

Os `.pyc` **ficam na imagem**. É o oposto da intuição de "imagem menor é sempre
melhor": um pod roda uma vez e morre, então compilar no import gastaria CPU em
toda invocação para economizar disco que já foi baixado.

## Go

`FROM scratch` — o binário, os certificados e um `/tmp`. `CGO_ENABLED=0` é o que
permite: com cgo o binário ficaria ligado à libc do sistema, que lá não existe.

`brevis/vendor_fake_go/` é o exemplo funcional. Ele fala com a API do BigQuery
por HTTP puro em vez de usar a biblioteca do Google — que multiplicaria o binário
para usar uma chamada. Mesma escolha do cliente do Kubernetes no Brevis.

## `shell: false` é obrigatório nas duas imagens enxutas

Nem distroless nem `scratch` têm `/bin/sh`. Sem a marca no passo, o comando iria
por `sh -c` e falharia com "no such file or directory" — erro correto e que não
diz nada sobre a causa.

```yaml
- id: fetch
  image: registry/fetch-x:0.1.0
  shell: false
  run: python3 /app/vendor_fake/fetch.py --linhas 500
```

Quem precisa de pipe, variável ou `&&` precisa de shell — e aí a imagem de dbt
(que tem um) é a escolha, ou a linha vira um script dentro do próprio programa.
