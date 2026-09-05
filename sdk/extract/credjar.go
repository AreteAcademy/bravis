package extract

import (
	"net/http"
	"net/url"
	"sync"
)

// credentialJar mantem a credencial FORA do jar e no cabecalho.
//
// O jar do Go casa cookie por prefixo de path, e um cookie sem Path herda o
// diretorio da URL que o originou. Com a fonte em /api/proxy/occurrences a
// credencial ficava presa a /api/proxy, e a renovacao em /api/auth/session ia
// sem ela -- o §9 do SDK_V9.md. Marcar Path=/ na semente resolve metade: o
// cookie REEMITIDO pela renovacao volta a ficar preso, agora em /api/auth, e
// as paginas seguem com o valor velho. Renovacao que nao alcanca as paginas
// nao renovou nada.
//
// Entao a credencial deixa de ser cookie de jar e passa a ser cabecalho, que
// vale para toda requisicao independente de path. O jar continua existindo
// para os outros cookies -- e a invariante da v0.26.0 se mantem: cada cookie
// mora num lugar so, e nenhum nome vai duas vezes.
//
// O efeito colateral util e o que o Store precisa: o valor rotacionado fica na
// mao, em vez de enterrado no jar.
type credentialJar struct {
	inner http.CookieJar

	// nomes sao os cookies que carregam a credencial. Fixo apos a montagem.
	nomes map[string]bool

	mu       sync.Mutex
	rotacoes map[string]string
}

func newCredentialJar(inner http.CookieJar, nomes []string) *credentialJar {
	j := &credentialJar{inner: inner, nomes: make(map[string]bool, len(nomes))}
	for _, n := range nomes {
		j.nomes[n] = true
	}
	return j
}

// SetCookies desvia o que e credencial e entrega o resto ao jar de verdade.
func (j *credentialJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	restantes := cookies[:0:0]
	for _, c := range cookies {
		if j.nomes[c.Name] {
			j.mu.Lock()
			if j.rotacoes == nil {
				j.rotacoes = map[string]string{}
			}
			j.rotacoes[c.Name] = c.Value
			j.mu.Unlock()
			continue
		}
		restantes = append(restantes, c)
	}
	if len(restantes) > 0 && j.inner != nil {
		j.inner.SetCookies(u, restantes)
	}
}

func (j *credentialJar) Cookies(u *url.URL) []*http.Cookie {
	if j.inner == nil {
		return nil
	}
	return j.inner.Cookies(u)
}

// Rotacoes devolve os valores reemitidos desde a ultima chamada, ou nil.
func (j *credentialJar) Rotacoes() map[string]string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.rotacoes) == 0 {
		return nil
	}
	out := j.rotacoes
	j.rotacoes = nil
	return out
}
