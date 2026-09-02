# Bravis Landing Page

Landing page moderna e minimalista para o projeto Bravis.

## Estrutura

```
site/
├── index.html          # Página principal
├── css/
│   └── styles.css      # Estilos globais
├── js/
│   └── main.js         # JavaScript funcionalidade
└── assets/             # Imagens, fonts, etc
```

## Desenvolvimento Local

### Servir localmente (Python 3)
```bash
cd site
python3 -m http.server 8000
```

Acesse: `http://localhost:8000`

### Servir localmente (Node.js)
```bash
npx http-server site
```

## Deploy

### Vercel (Recomendado)
```bash
npm i -g vercel
vercel site/
```

### Netlify
```bash
npm i -g netlify-cli
netlify deploy --dir=site
```

### GitHub Pages
1. Habilite GitHub Pages nas settings do repo
2. Configure o branch como `main` e pasta como `site/`

### Docker
```bash
docker run -d -p 80:80 -v $(pwd)/site:/usr/share/nginx/html nginx:alpine
```

## Características

✨ **Design Moderno**
- Paleta de cores sofisticada (pergaminho + ouro)
- Tipografia elegante com sistema de escalas
- Dark mode automático
- Animações sutis (fade-in, hover effects)

🚀 **Performance**
- System fonts (sem lag de web fonts)
- CSS modular e otimizado
- Minimalista e carregamento rápido
- Responsivo (mobile-first)

♿ **Acessibilidade**
- Suporta `prefers-reduced-motion`
- Focus states visíveis
- Links com `target="_blank"` e `rel="noopener noreferrer"`
- Semântica HTML correta

🎯 **SEO**
- Meta tags descritivas
- Schema.org pronto para implementação
- URLs limpas e semânticas

## Personalização

### Cores
Edite o CSS em `css/styles.css` na seção `:root`:

```css
:root {
  --accent: #aa8450;
  --bg: #f4efe4;
  --text-primary: #21180f;
  /* ... */
}
```

### Conteúdo
Edite `index.html` diretamente para atualizar textos, links e seções.

### Fontes
Atualmente usa:
- **Serif**: Lora (do Google Fonts)
- **Sans**: System fonts (Apple, Windows, Linux nativas)

Para adicionar outra fonte, atualize o `<link>` no `<head>`:

```html
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=...">
```

## Temas

### Light Mode (Padrão)
Pergaminho (#f4efe4) com ouro (#aa8450)

### Dark Mode (Automático)
Cinza escuro (#1a1815) com dourado (#d4b896)

O site detecta preferência do SO automaticamente. Use DevTools para testar: 
- Chrome: `⌘+Shift+P` → "Render" → "Emulate CSS media feature prefers-color-scheme"

## Analytics

Para adicionar analytics (Plausible, Fathom, Umami):

```html
<!-- Em index.html, antes de </body> -->
<script defer data-domain="bravis.sh" src="https://plausible.io/js/script.js"></script>
```

## Links Úteis

- GitHub: https://github.com/zarvhq/bravis
- Docs: https://github.com/zarvhq/bravis/tree/main/docs
- Discussões: https://github.com/zarvhq/bravis/discussions

## Licença

MIT