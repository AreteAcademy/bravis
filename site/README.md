# Landing page do brevis.sh

HTML, CSS e JS estáticos. Sem build, sem dependência, sem `node_modules` —
o que está no diretório é o que vai para o ar.

```
site/
├── index.html          # a página inteira, dez seções
├── css/styles.css      # tokens + componentes
├── js/main.js          # menu e entrada dos blocos
└── assets/favicon.svg  # a marca, com fundo sólido
```

Total servido: ~12,5 KB gzip (HTML + CSS + JS), fora as fontes do Google Fonts.

## Rodar

```bash
python3 -m http.server 8000   # ou: npx http-server .
```

## Identidade

Escura por decisão, não por preferência do sistema: a página é um terminal
editorial, e um modo claro seria outro projeto. Por isso **não há bloco
`prefers-color-scheme`** — há um `color-scheme: dark` declarado, para que os
controles nativos do navegador acompanhem.

| token | valor | uso |
|---|---|---|
| `--bg` | `#141711` | fundo |
| `--surface` | `#1a1e15` | superfícies operacionais |
| `--surface-2` | `#1f2419` | superfície em destaque |
| `--green-2` | `#38412b` | filete estrutural |
| `--lime` | `#c7d66d` | acento e estado ativo |
| `--olive` | `#7d8e50` | rótulos e detalhes |
| `--text` | `#ecefe4` | texto principal |
| `--text-dim` | `#a3ad97` | texto secundário |

Contraste medido sobre o fundo: texto **15,6:1**, secundário **7,7:1**, lima
**11,4:1**, oliva **5,1:1**. O `--green-2` rende **1,7:1** — ele é filete
estrutural e nada mais. Nunca use para texto nem para contorno de controle;
para isso existe o oliva.

Tipografia: **Cormorant Garamond** nos títulos, **IBM Plex Sans** no texto,
**IBM Plex Mono** em rótulos, comandos e dados.

Os rótulos de seção usam o prefixo `//`, e ele vem do **CSS**
(`.eyebrow::before`), não do HTML — assim um leitor de tela não anuncia "barra
barra" antes de cada seção.

## Estrutura

Dez seções, na ordem: header · hero · contraste de visão · produto ·
princípios · open source · filosofia · FAQ · CTA · footer.

Um único `<h1>`, na hero. Cada seção abre com `<h2>`; os cards internos usam
`<h3>`. O rótulo acima do título é um `<p class="eyebrow">`, **não** um
heading — ele é um rótulo, e promovê-lo a `h2` roubaria o nível do título real.

## Armadilhas já encontradas

Ao editar, quatro coisas que quebram em silêncio:

- **`[hidden]` precisa do `!important`.** O `hidden` do UA vale menos que
  qualquer seletor de classe, e vários blocos aqui declaram `display`.
- **Em grid, use `minmax(0, 1fr)`, nunca `1fr`.** `1fr` é `minmax(auto, 1fr)`:
  o track não encolhe abaixo do min-content do filho, e um `<pre>` de código
  empurra a página inteira para a rolagem horizontal no celular. Nos `auto-fit`,
  a forma segura é `minmax(min(280px, 100%), 1fr)`.
- **`gap` no `.brand` separa a marca do TLD.** O texto precisa estar num
  `<span>` único, senão o flex trata `brevis` e `.sh` como dois itens e a logo
  vira `brevis .sh`.
- **`margin-top: auto` só funciona no flex item.** Um botão dentro de um `<p>`
  não é o item; quem recebe é o `<p>`.

## Acessibilidade

Alvo do Lighthouse CI: performance ≥ 0,9 e acessibilidade ≥ 0,95
(`.lighthouserc.json`). O que sustenta isso:

- contraste AA em todo texto, incluindo os tokens de sintaxe do bloco de código;
- **FAQ em `<details>` + `<summary>`** — accordion nativo, operável por teclado,
  sem uma linha de script;
- `data-reveal` só esconde quando a classe `.js` confirma que o script rodou —
  uma falha de JS não apaga a página;
- `prefers-reduced-motion` desliga animação, o pulso do separador e a rolagem
  suave;
- skip link, `<main>`, e hierarquia de headings sem salto.

## SEO

`title`, `description`, Open Graph, Twitter Card e **JSON-LD** com um `@graph`
de `WebSite`, `SoftwareSourceCode` e `Organization`. O JSON-LD declara apenas o
que é verificável: repositório, linguagem, licença MIT e autoria. Sem métricas,
sem contagem de comunidade, sem clientes.

## Deploy

`netlify.toml`, `vercel.json` e `Dockerfile` (nginx) já estão no diretório, com
os redirects `/github`, `/docs`, `/issues` e `/discussions`. A CI
(`.github/workflows/build-site.yml`) valida, roda Lighthouse no PR e publica a
imagem no push.

```bash
vercel .                       # ou: netlify deploy --dir=.
docker build -t brevis-site .  # o que a CI faz
```

## Analytics

Não há nenhum. Para adicionar, uma linha antes de `</body>`:

```html
<script defer data-domain="brevis.sh" src="https://plausible.io/js/script.js"></script>
```
