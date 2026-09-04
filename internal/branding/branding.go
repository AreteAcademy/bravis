// Package branding carrega a identidade visual da instalacao.
//
// Existe porque a interface vai ser usada por clientes diferentes, e cada um
// quer o proprio nome, a propria frase e as proprias cores. O que NAO se
// customiza e a atribuicao "Powered by Brevis": ela nao vem da configuracao,
// vem do codigo, e por isso nao ha valor de YAML capaz de removê-la.
//
// A escolha por YAML segue o resto do projeto — workflows sao YAML, e um segundo
// mecanismo de configuracao (banco, painel, variaveis de ambiente para vinte
// cores) seria um jeito novo de fazer a mesma coisa.
package branding

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Marca e a identidade de uma instalacao.
type Marca struct {
	// Titulo aparece na barra lateral e no <title> das paginas.
	Titulo string `yaml:"titulo"`

	// Subtitulo e o versalete sob o titulo.
	Subtitulo string `yaml:"subtitulo"`

	// Frase e a citacao do rodape da barra lateral. Multiplas linhas sao
	// preservadas — a quebra faz parte do ritmo do texto.
	Frase string `yaml:"frase"`

	// Logo e a marca grafica ao lado do titulo. Aceita URL absoluta (a logo
	// hospedada do cliente) ou caminho interno comecando em `/assets/`.
	//
	// Vazio cai no simbolo embutido, e o padrao e interno de proposito: uma
	// logo que depende de host externo some quando aquele host cai, quando o
	// cluster nao tem saida para a internet, ou quando o cliente reorganiza o
	// proprio site. A tela de uma ferramenta de operacao nao pode quebrar por
	// causa disso.
	Logo string `yaml:"logo"`

	Tema Tema `yaml:"tema"`
}

// Tema sao as cores. Cada campo mapeia para uma variavel CSS que o Tailwind ja
// emite; sobrescreve-las em tempo de execucao repinta a interface inteira sem
// recompilar CSS, porque TODO utilitario resolve a cor por `var(--color-*)`.
type Tema struct {
	Fundo         string `yaml:"fundo"`
	FundoSuave    string `yaml:"fundo_suave"`
	Superficie    string `yaml:"superficie"`
	Tinta         string `yaml:"tinta"`
	TextoSuave    string `yaml:"texto_suave"`
	Destaque      string `yaml:"destaque"`
	DestaqueForte string `yaml:"destaque_forte"`

	Sucesso    string `yaml:"sucesso"`
	Falha      string `yaml:"falha"`
	Executando string `yaml:"executando"`
	Fila       string `yaml:"fila"`
	Repetindo  string `yaml:"repetindo"`
	Cancelado  string `yaml:"cancelado"`
	Aguardando string `yaml:"aguardando"`
}

// LogoPadrao e o simbolo embutido, servido do proprio binario.
const LogoPadrao = "/assets/logo.svg"

// Atribuicao e fixa. Nao e campo de configuracao de proposito: e a unica coisa
// da tela que o cliente nao escolhe.
const Atribuicao = "Powered by Brevis"

// Padrao e a identidade Arete, usada quando nao ha arquivo de marca.
func Padrao() Marca {
	return Marca{
		Titulo:    "Brevis",
		Subtitulo: "Orquestração",
		Logo:      LogoPadrao,
		Frase:     "Clareza, estrutura e virtude\ntambém fazem parte\nde quem constrói.",
		Tema: Tema{
			Fundo:         "#f4efe4",
			FundoSuave:    "#fbf8f1",
			Superficie:    "#fffdf8",
			Tinta:         "#21180f",
			TextoSuave:    "#6e6254",
			Destaque:      "#aa8450",
			DestaqueForte: "#8a693d",
			Sucesso:       "#4c7a56",
			Falha:         "#b0503c",
			Executando:    "#3f6d8f",
			Fila:          "#b3822f",
			Repetindo:     "#a35f28",
			Cancelado:     "#8a8175",
			Aguardando:    "#a89b8a",
		},
	}
}

// Carregar le o arquivo de marca. Ausencia NAO e erro: a instalacao padrao nao
// tem arquivo nenhum, e exigi-lo faria o container falhar no boot por causa de
// uma customizacao opcional.
//
// Campos ausentes herdam o padrao, entao um arquivo com duas linhas — so o nome
// e a frase — e um arquivo valido.
func Carregar(caminho string) (Marca, error) {
	m := Padrao()
	if caminho == "" {
		return m, nil
	}
	conteudo, err := os.ReadFile(caminho)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("lendo %s: %w", caminho, err)
	}
	// Decodifica SOBRE o padrao: o yaml.v3 so escreve os campos presentes no
	// arquivo, entao o resto permanece.
	if err := yaml.Unmarshal(conteudo, &m); err != nil {
		return Padrao(), fmt.Errorf("%s: yaml invalido: %w", caminho, err)
	}
	if err := m.Validar(); err != nil {
		return Padrao(), fmt.Errorf("%s: %w", caminho, err)
	}
	return m, nil
}

var hex = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// Validar recusa cor que nao seja hexadecimal.
//
// Isto e seguranca, nao purismo: os valores do tema sao escritos dentro de um
// bloco <style> na pagina. Uma string livre ali poderia fechar a declaracao e
// injetar CSS arbitrario — que, num painel de operacao, e capaz de esconder um
// estado de falha atras de um seletor.
func (m Marca) Validar() error {
	if strings.TrimSpace(m.Titulo) == "" {
		return fmt.Errorf("titulo nao pode ser vazio")
	}
	for nome, cor := range m.Tema.cores() {
		if !hex.MatchString(cor) {
			return fmt.Errorf("cor %s: %q nao e um hexadecimal (#rgb, #rrggbb ou #rrggbbaa)", nome, cor)
		}
	}
	if err := validarLogo(m.Logo); err != nil {
		return err
	}
	return nil
}

// validarLogo aceita apenas https://, http:// e caminho interno.
//
// Pelo mesmo motivo das cores: o valor vai para o `src` de uma <img>. Um
// `javascript:` ou um `data:text/html,...` ali executa script na sessao de quem
// abriu o painel — e quem edita o arquivo de marca pode nao ser quem opera o
// cluster. A lista e de permissao, nao de bloqueio: recusar `javascript:` por
// nome deixa passar o proximo esquema que alguem inventar.
func validarLogo(logo string) error {
	if logo == "" {
		return nil
	}
	if strings.HasPrefix(logo, "/") && !strings.HasPrefix(logo, "//") {
		return nil
	}
	if strings.HasPrefix(logo, "https://") || strings.HasPrefix(logo, "http://") {
		return nil
	}
	return fmt.Errorf("logo %q: use https://, http:// ou um caminho interno "+
		"comecando em /", logo)
}

func (t Tema) cores() map[string]string {
	return map[string]string{
		"fundo": t.Fundo, "fundo_suave": t.FundoSuave, "superficie": t.Superficie,
		"tinta": t.Tinta, "texto_suave": t.TextoSuave,
		"destaque": t.Destaque, "destaque_forte": t.DestaqueForte,
		"sucesso": t.Sucesso, "falha": t.Falha, "executando": t.Executando,
		"fila": t.Fila, "repetindo": t.Repetindo, "cancelado": t.Cancelado,
		"aguardando": t.Aguardando,
	}
}

// CSS devolve as variaveis a injetar no <head>.
//
// Vazio quando o tema e o padrao: a folha compilada ja tem esses valores, e
// repeti-los seria bytes em toda pagina para nao mudar nada.
func (m Marca) CSS() string {
	padrao := Padrao().Tema
	if m.Tema == padrao {
		return ""
	}

	var b strings.Builder
	b.WriteString(":root{")
	escreve := func(variavel, valor string) {
		if valor != "" {
			fmt.Fprintf(&b, "%s:%s;", variavel, valor)
		}
	}
	escreve("--color-parchment", m.Tema.Fundo)
	escreve("--color-parchment-soft", m.Tema.FundoSuave)
	escreve("--color-surface", m.Tema.Superficie)
	escreve("--color-ink", m.Tema.Tinta)
	escreve("--color-muted", m.Tema.TextoSuave)
	escreve("--color-gold", m.Tema.Destaque)
	escreve("--color-gold-strong", m.Tema.DestaqueForte)

	// Derivadas: linha e realce sao a mesma cor com transparencia. Calcular
	// aqui, e nao pedir ao cliente, evita que ele configure uma borda que nao
	// combina com a propria tinta que escolheu.
	escreve("--color-line", comAlfa(m.Tema.Tinta, "1a"))
	escreve("--color-line-soft", comAlfa(m.Tema.Tinta, "0d"))
	escreve("--color-gold-wash", comAlfa(m.Tema.Destaque, "14"))

	escreve("--color-state-success", m.Tema.Sucesso)
	escreve("--color-state-failed", m.Tema.Falha)
	escreve("--color-state-running", m.Tema.Executando)
	escreve("--color-state-queued", m.Tema.Fila)
	escreve("--color-state-retrying", m.Tema.Repetindo)
	escreve("--color-state-canceled", m.Tema.Cancelado)
	escreve("--color-state-pending", m.Tema.Aguardando)

	// O fundo do body e um degrade escrito a mao no CSS-fonte, entao nao segue
	// as variaveis sozinho.
	fmt.Fprintf(&b, "}body{background:linear-gradient(180deg,%s 0%%,%s 44%%,%s 100%%);}",
		m.Tema.FundoSuave, m.Tema.Fundo, m.Tema.FundoSuave)
	return b.String()
}

// comAlfa anexa o canal alfa a uma cor de 6 digitos. Formatos curtos ou que ja
// trazem alfa sao devolvidos intactos — misturar canais daria uma cor errada
// em vez de um erro visivel.
func comAlfa(cor, alfa string) string {
	if len(cor) != 7 {
		return cor
	}
	return cor + alfa
}

// Linhas quebra a frase para o template. A quebra e do autor, e transformá-la em
// espaco mudaria o ritmo do texto na barra lateral.
func (m Marca) Linhas() []string {
	var out []string
	for _, l := range strings.Split(m.Frase, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

type chave struct{}

// EmContexto injeta a marca no contexto da requisicao.
//
// Contexto, e nao parametro de cada template: toda pagina precisa da marca, e
// acrescentá-la a assinatura de dez componentes so para chegar ao layout base
// tornaria cada tela nova uma chance de esquecer.
func EmContexto(ctx context.Context, m Marca) context.Context {
	return context.WithValue(ctx, chave{}, m)
}

// De recupera a marca. Sem marca no contexto — um teste que renderiza direto, um
// caminho que nao passou pelo middleware — devolve o padrao em vez de uma tela
// sem nome nenhum.
func De(ctx context.Context) Marca {
	if m, ok := ctx.Value(chave{}).(Marca); ok {
		return m
	}
	return Padrao()
}
