// Package assets embute os arquivos estaticos no binario.
//
// Servir do sistema de arquivos (`http.Dir("web/assets")`) quebrava em duas
// situacoes: no container distroless, que copia apenas o binario, e ao rodar o
// `bravis` de qualquer diretorio que nao a raiz do repo. Em ambas o CSS dava 404
// e a UI aparecia sem estilo — que parece defeito da pagina, nao do caminho.
//
// Embutir resolve as duas de uma vez, e segue o que ja e feito com as migrations:
// o binario carrega tudo de que precisa.
package assets

import "embed"

// FS contem o CSS compilado. O `app.src.css` fica de fora de proposito — e
// entrada do Tailwind, nao artefato servido.
//
//go:embed app.css
var FS embed.FS
