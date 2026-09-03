# Landing page do Bravis

HTML, CSS e JS estáticos. Sem build, sem dependência, sem `node_modules` —
o que está no diretório é o que vai para o ar.

```
site/
├── index.html          # a página inteira, uma coluna de âncoras
├── css/styles.css      # tokens + componentes
├── js/main.js          # tema, abas, copiar, menu, reveal
└── assets/favicon.svg  # o grafo do logo, com cor fixa
```

Total servido: ~19 KB gzip (HTML + CSS + JS), fora as duas fontes do Google
Fonts.

## Rodar

```bash
python3 -m http.server 8000   # ou: npx http-server .
```

## Identidade

A paleta e a tipografia são as da [Aretê Academy](https://areteacademy.com.br)
e as mesmas da UI do binário (`web/assets/app.src.css`) — os valores são
idênticos, não aproximações. Trocar de superfície não deve parecer trocar de
produto.

| token | claro | escuro |
|---|---|---|
| `--bg` | `#f4efe4` | `#1a1512` |
| `--surface` | `#fffdf8` | `#29221b` |
| `--text` | `#21180f` | `#f4efe4` |
| `--muted` | `#6e6254` | `#b3a794` |
| `--gold` | `#aa8450` | `#d4b896` |
| `--gold-ink` | `#7d5e35` | `#dcc3a2` |

**`--gold` e `--gold-ink` existem separados por contraste.** O ouro da marca
(`#aa8450`) rende 2,99:1 sobre o pergaminho: serve para borda, ícone e filete,
nunca para texto pequeno. Texto em ouro usa `--gold-ink` (5,19:1, AA). Ao mexer
na paleta, mantenha essa divisão.

Tipografia: **Cormorant Garamond** nos títulos, **Inter** no texto, e a mono do
sistema no código — a mono não é baixada, o que tira uma requisição do caminho
crítico.

Os blocos de código são painéis escuros nos **dois** temas, como o
`.hero-panel` do site do grupo. É um conjunto único de tokens de sintaxe sobre
um fundo único, em vez de dois esquemas para manter em paralelo.

## Tema

Três estados: `data-theme="light"`, `data-theme="dark"` e nenhum dos dois —
que segue o `prefers-color-scheme` do sistema. A escolha do visitante vai para
`localStorage` (`bravis-theme`) e é reaplicada por um script no `<head>`, antes
da primeira pintura; sem ele, quem escolheu o escuro vê o pergaminho por um
frame a cada carregamento.

## Armadilhas já encontradas

Ao editar, três coisas que quebram em silêncio:

- **`[hidden]` precisa do `!important`.** O `hidden` do UA stylesheet vale menos
  que qualquer seletor de classe, e `.install-row`/`.panel` definem `display` —
  sem a regra, todos os painéis de aba aparecem de uma vez.
- **Em grid, use `minmax(0, 1fr)`, nunca `1fr`.** `1fr` é `minmax(auto, 1fr)`:
  o track não encolhe abaixo do min-content do filho, e um `<pre>` de código
  empurra a página inteira para a rolagem horizontal no celular.
- **Cormorant usa numerais oldstyle.** Em métrica isso faz o `1` de "1 binário"
  ler como `I`. Os números do bloco de stats forçam
  `font-variant-numeric: lining-nums`.

## Acessibilidade

Alvo do Lighthouse CI: performance ≥ 0,9 e acessibilidade ≥ 0,95
(`.lighthouserc.json`). O que sustenta isso:

- contraste AA em todo texto, incluindo os tokens de sintaxe sobre o painel escuro;
- abas com o padrão ARIA completo (`role`, `aria-selected`, setas, Home/End);
- `data-reveal` só esconde quando a classe `.js` confirma que o script rodou —
  uma falha de JS não apaga a página;
- `prefers-reduced-motion` desliga animação e rolagem suave;
- skip link, `<main>`, e ordem de headings sem salto.

## Deploy

`netlify.toml`, `vercel.json` e `Dockerfile` (nginx) já estão no diretório, com
os redirects `/github`, `/docs`, `/issues` e `/discussions`. A CI
(`.github/workflows/build-site.yml`) valida, roda Lighthouse no PR e publica a
imagem no push.

```bash
vercel .                       # ou: netlify deploy --dir=.
docker build -t bravis-site .  # o que a CI faz
```

## Analytics

Não há nenhum. Para adicionar, uma linha antes de `</body>`:

```html
<script defer data-domain="bravis.sh" src="https://plausible.io/js/script.js"></script>
```
