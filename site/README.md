# Site do brevis.sh

Landing page e documentação, em português e inglês. HTML estático **gerado** por
um script Python sem dependências — nada de Node, nada de `node_modules`.

```bash
python3 build.py           # gera 28 páginas em ~0,3 s
python3 build.py --check   # falha se o HTML commitado estiver desatualizado
```

O HTML gerado **vai commitado**. O deploy serve arquivos estáticos e não roda
nada; o `--check` na CI é o que garante que o commitado corresponde às fontes.

## O que é fonte e o que é gerado

| fonte — edite | gerado — não edite |
|---|---|
| `build.py` | `index.html`, `404.html` |
| `templates/*.html` | `docs/**/index.html` |
| `i18n.json` | `en/**` |
| `content/<lang>/docs/*.md` | `sitemap.xml`, `robots.txt`, `search-index.json` |
| `css/*.css`, `js/*.js`, `assets/*` | |

## Idiomas

O padrão fica na raiz; os demais ganham prefixo. Trocar `PADRAO` no `build.py`
troca quem fica em `/` — e nada mais precisa mudar.

```
brevis.sh/            → pt-BR      brevis.sh/en/           → inglês
brevis.sh/docs/       → pt-BR      brevis.sh/en/docs/      → inglês
```

Cada página declara `hreflang` para os dois idiomas mais `x-default`. O seletor
no topo leva ao **mesmo lugar** no outro idioma, não à home.

**Os slugs são iguais nos dois idiomas** (`/docs/installation/` e
`/en/docs/installation/`). Só os títulos são traduzidos. Isso é o que permite o
`hreflang` apontar de uma página para a sua correspondente sem uma tabela de
tradução de URLs.

Dentro do Markdown, escreva sempre `/docs/x/`: o gerador acrescenta o prefixo do
idioma quando ele não é o padrão.

## Escrevendo uma página

Um arquivo `.md` em `content/<lang>/docs/`, com front matter:

```markdown
---
title: Instalação
description: Uma frase — vira o subtítulo, a meta description e o texto do card.
group: Começar
order: 2
slug: installation
---

Conteúdo.
```

`order` define a posição na barra lateral; `group` define o agrupamento, na
ordem em que os grupos aparecem. **Adicione a página nos dois idiomas** — o
`hreflang` supõe que a correspondente existe.

O número no nome do arquivo (`02-installation.md`) é só para o `ls` ficar
legível; quem manda é o `order`.

### Markdown suportado

Um subconjunto deliberado: heading (`##` e `###` entram no índice lateral),
parágrafo, lista, tabela, citação, cerca de código, regra e admonição. **HTML
embutido não é aceito** — um parser que aceita tudo é um parser que aceita erro.

````markdown
:::note Título opcional
Uma nota. Também há `tip`, `warning` e `danger`.
:::

```yaml
name: exemplo   # realce para bash, yaml, go, json e sql
```
````

## Busca

`search-index.json` é gerado com título, descrição e os primeiros 1400
caracteres de cada página, por idioma. A busca é client-side, filtra pelo idioma
da página e pontua título acima de corpo. Sem serviço externo, sem chave de API.

`/` foca o campo, como em qualquer documentação.

## Armadilhas já encontradas

- **Restaure placeholders do último para o primeiro.** Em `inline()`, um link
  com código dentro — ``[`serve`](#serve)`` — guarda o `<code>` como
  placeholder 0 e o `<a>` que o contém como 1. Restaurando 0 antes de 1, o 0
  ainda não está no texto, e o `\x00` some na renderização deixando o dígito: a
  coluna inteira vira "0".
- **`[hidden]` precisa de `!important`.** O `hidden` do UA vale menos que
  qualquer seletor de classe.
- **Em grid, `minmax(0, 1fr)`, nunca `1fr`.** `1fr` é `minmax(auto, 1fr)`: o
  track não encolhe abaixo do min-content, e um `<pre>` empurra a página inteira
  para a rolagem horizontal no celular.
- **`{{ .campo }}` nos exemplos é template do Brevis**, não do gerador. O
  gerador só troca `{{ identificador }}`; um placeholder seu sem valor levanta
  `KeyError` no build, em vez de sobreviver até o HTML.

## Identidade

Escura por decisão, não por preferência do sistema. Contraste medido sobre o
fundo `#141711`: texto **15,6:1**, secundário **7,7:1**, lima **11,4:1**, oliva
**5,1:1**. O `--green-2` rende **1,7:1** — filete estrutural e nada mais.

Tipografia: Cormorant Garamond nos títulos, IBM Plex Sans no texto, IBM Plex
Mono em rótulos e código.

## Acessibilidade

- um `<h1>` por página, sem salto de nível;
- FAQ em `<details>`/`<summary>` — accordion nativo, sem script;
- `prefers-reduced-motion` desliga animação e rolagem suave;
- `data-reveal` só esconde quando a classe `.js` confirma que o script rodou;
- skip link, `<main>`, foco visível.

## Deploy

O `Dockerfile` (nginx) remove as fontes de build da imagem e devolve **404 de
verdade** para caminho inexistente — `try_files … /index.html` serviria a
landing com status 200, e um link quebrado passaria despercebido.

```bash
docker build -t brevis-site .
vercel .                        # ou: netlify deploy --dir=.
```

A CI (`.github/workflows/build-site.yml`) roda `build.py --check`, valida o HTML
das 28 páginas, roda Lighthouse no PR e publica a imagem no push.

## Documentação futura em `docs.brevis.sh`

O gerador já produz tudo sob `/docs/`. Para mover, aponte o subdomínio para o
mesmo artefato e ajuste `BASE_URL` no `build.py` — os links internos são
absolutos a partir da raiz, então nada mais muda.
