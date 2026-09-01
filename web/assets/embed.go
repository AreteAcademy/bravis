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

// FS contem o CSS compilado e os scripts da ilha React. O `app.src.css` fica de
// fora de proposito — e entrada do Tailwind, nao artefato servido.
//
// O `vendor/` guarda React, ReactDOM e React Flow em UMD (~350 KB). Vendorizar
// em vez de apontar para um CDN e escolha da secao 15: sem npm no build, sem
// dependencia de rede externa em runtime, e a UI continua funcionando num
// cluster sem saida para a internet.
//
// As fontes (Inter e Cormorant Garamond, ~205 KB) entram pelo mesmo motivo dos
// bundles: uma UI que depende do Google Fonts troca de tipografia no meio da
// tela quando a rede nao responde.
//
//go:embed app.css ui.js dag.js jsx-shim.js logo.svg vendor fonts
var FS embed.FS

// LogoSVG e a marca padrao ja lida do FS embutido.
//
// Existe como string, e nao apenas como arquivo servido, porque o simbolo e
// desenhado em `currentColor`: dentro de uma <img> o SVG e um documento
// isolado e nao herda cor nenhuma da pagina, entao sairia preto em qualquer
// tema. Inline, ele acompanha a paleta do cliente.
var LogoSVG = func() string {
	b, err := FS.ReadFile("logo.svg")
	if err != nil {
		// Impossivel em producao: o arquivo e embutido no binario e a
		// compilacao falha sem ele. Vazio degrada para "sem logo".
		return ""
	}
	return string(b)
}()
