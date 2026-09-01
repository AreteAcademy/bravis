package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Param e um parametro de execucao: o que muda entre dois disparos do MESMO
// workflow sem editar o arquivo.
//
// Era a maior distancia entre este motor e o Kestra/Leoflow. Sem params nao ha
// backfill (`load_full=true`) nem reprocessamento de janela, e oito dos 51 flows
// do repositorio de dados sequer podiam ser convertidos — o comando deles carrega
// `{{ inputs.start_date }}` e afins.
type Param struct {
	Nome      string
	Tipo      TipoParam
	Padrao    string
	Descricao string

	// Enum restringe os valores aceitos. Vazio = qualquer um que passe no tipo.
	Enum []string

	// Pattern e uma expressao regular que o valor precisa casar. Existe para o
	// autor AMPLIAR o que o tipo `string` aceita por padrao — ver `seguro`.
	Pattern string
}

type TipoParam string

const (
	ParamTexto   TipoParam = "string"
	ParamBool    TipoParam = "boolean"
	ParamInteiro TipoParam = "integer"
)

// caracteresSeguros e o que um `string` aceita quando o autor nao declara
// `pattern`.
//
// Isto e defesa contra injecao de shell, nao purismo: o valor de um param vai
// PARA DENTRO da linha de comando do passo, e quem dispara um run nao e
// necessariamente quem escreveu o workflow. Sem a restricao,
// `--date {{ .data }}` com `data = "; rm -rf /"` seria execucao arbitraria no
// worker.
//
// O conjunto cobre o que os params reais deste repositorio precisam — datas,
// selectors do dbt, uids, caminhos, listas separadas por virgula — e deixa de
// fora tudo que o shell interpreta: aspas, `;`, `|`, `&`, `$`, crase,
// parenteses e redirecionamentos.
var caracteresSeguros = regexp.MustCompile(`^[A-Za-z0-9_.:/=,+@\- ]*$`)

var nomeDeParam = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validar confere a declaracao do param, nao o valor.
func (p Param) Validar() error {
	if !nomeDeParam.MatchString(p.Nome) {
		return fmt.Errorf("param %q: o nome deve ser minusculo, comecar por letra e conter apenas letras, digitos e _", p.Nome)
	}
	switch p.Tipo {
	case ParamTexto, ParamBool, ParamInteiro:
	case "":
		return fmt.Errorf("param %q sem tipo (string, boolean ou integer)", p.Nome)
	default:
		return fmt.Errorf("param %q: tipo %q desconhecido", p.Nome, p.Tipo)
	}
	if p.Pattern != "" {
		if _, err := regexp.Compile(p.Pattern); err != nil {
			return fmt.Errorf("param %q: pattern invalido: %w", p.Nome, err)
		}
	}
	// O padrao precisa ser valido pelas proprias regras: um default recusado so
	// apareceria no primeiro disparo agendado, de madrugada.
	if p.Padrao != "" {
		if err := p.Aceita(p.Padrao); err != nil {
			return fmt.Errorf("param %q: valor padrao invalido: %w", p.Nome, err)
		}
	}
	return nil
}

// Aceita valida um VALOR contra a declaracao.
func (p Param) Aceita(valor string) error {
	switch p.Tipo {
	case ParamBool:
		if valor != "true" && valor != "false" {
			return fmt.Errorf("%q nao e booleano (use true ou false)", valor)
		}
		return nil
	case ParamInteiro:
		if _, err := strconv.Atoi(valor); err != nil {
			return fmt.Errorf("%q nao e inteiro", valor)
		}
		return nil
	}

	if len(p.Enum) > 0 {
		for _, permitido := range p.Enum {
			if valor == permitido {
				return nil
			}
		}
		return fmt.Errorf("%q nao esta entre os valores aceitos (%s)", valor, strings.Join(p.Enum, ", "))
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return err
		}
		if !re.MatchString(valor) {
			return fmt.Errorf("%q nao casa com o pattern %q", valor, p.Pattern)
		}
		return nil
	}
	if !caracteresSeguros.MatchString(valor) {
		return fmt.Errorf("%q tem caractere que o shell interpreta; "+
			"declare um `pattern` no param se o valor precisa mesmo dele", valor)
	}
	return nil
}

// Resolver mistura os valores informados com os padroes e valida tudo.
//
// Chave desconhecida e ERRO, nao silencio: `--param lod_full=true` com typo
// rodaria o workflow com o padrao e ninguem perceberia que o backfill nao
// aconteceu.
func (w Workflow) Resolver(informados map[string]string) (map[string]string, error) {
	declarados := make(map[string]Param, len(w.Params))
	for _, p := range w.Params {
		declarados[p.Nome] = p
	}

	for nome := range informados {
		if _, existe := declarados[nome]; !existe {
			return nil, fmt.Errorf("workflow %q nao declara o param %q (declarados: %s)",
				w.Slug, nome, nomesDe(w.Params))
		}
	}

	out := make(map[string]string, len(w.Params))
	for _, p := range w.Params {
		valor, informado := informados[p.Nome]
		if !informado {
			valor = p.Padrao
		}
		if err := p.Aceita(valor); err != nil {
			return nil, fmt.Errorf("param %q: %w", p.Nome, err)
		}
		out[p.Nome] = valor
	}
	return out, nil
}

func nomesDe(ps []Param) string {
	if len(ps) == 0 {
		return "nenhum"
	}
	nomes := make([]string, len(ps))
	for i, p := range ps {
		nomes[i] = p.Nome
	}
	return strings.Join(nomes, ", ")
}
