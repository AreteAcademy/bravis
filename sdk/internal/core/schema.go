package core

import (
	"fmt"
	"sort"
	"strings"
)

// ColumnType e o tipo de uma coluna declarada.
//
// A lista e curta de proposito. Ela nao e o sistema de tipos de nenhum banco:
// e o conjunto minimo que um registro JSON produz, com um nome so para cada
// coisa. Quem precisa de NUMERIC(18,2), de VARCHAR com tamanho ou de um tipo
// que so um destino tem escreve o DDL em CreateSQL -- que continua existindo
// exatamente para isso.
type ColumnType string

const (
	// TypeString e texto. No BigQuery, STRING.
	TypeString ColumnType = "string"

	// TypeInt64 e inteiro de 64 bits.
	TypeInt64 ColumnType = "int64"

	// TypeFloat64 e ponto flutuante. NAO use para dinheiro: um float perde
	// centavos em valores grandes, e o prejuizo aparece meses depois num
	// relatorio que ninguem confere. Para dinheiro, TypeNumeric.
	TypeFloat64 ColumnType = "float64"

	// TypeNumeric e decimal exato.
	TypeNumeric ColumnType = "numeric"

	// TypeBool e booleano.
	TypeBool ColumnType = "bool"

	// TypeTimestamp e instante com fuso.
	TypeTimestamp ColumnType = "timestamp"

	// TypeDate e data sem hora.
	TypeDate ColumnType = "date"

	// TypeJSON e um documento aninhado.
	TypeJSON ColumnType = "json"

	// TypeBytes e binario.
	TypeBytes ColumnType = "bytes"
)

// Column e uma coluna declarada: o nome, o tipo, e se ela aceita nulo.
type Column struct {
	// Name e o nome da coluna no destino. Obrigatorio.
	Name string

	// Type e o tipo. Obrigatorio -- um tipo em branco seria uma inferencia
	// disfarcada de declaracao.
	Type ColumnType

	// Required marca NOT NULL. O padrao e aceitar nulo, que e o que uma
	// tabela de landing legitimamente faz: uma coluna que a fonte as vezes
	// nao manda.
	Required bool
}

// Schema e a declaracao do destino, na ordem do DDL.
//
// Ela existe porque o SDK NAO infere tipo. Um destino que oferecesse
// autodetect faria os tipos sairem do dado -- e o dado de uma execucao nao e o
// dado da proxima: um campo que veio inteiro hoje e decimal amanha muda o tipo
// da coluna sem ninguem escrever nada.
//
//	Schema: sdk.Schema{
//	    {Name: "ingestion_id",        Type: sdk.TypeString,    Required: true},
//	    {Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
//	    {Name: "temperatura",         Type: sdk.TypeFloat64},
//	}
type Schema []Column

// Names devolve os nomes, na ordem declarada.
func (s Schema) Names() []string {
	out := make([]string, len(s))
	for i, c := range s {
		out[i] = c.Name
	}
	return out
}

// Has diz se a coluna esta declarada.
func (s Schema) Has(nome string) bool {
	for _, c := range s {
		if c.Name == nome {
			return true
		}
	}
	return false
}

// Check recusa uma declaracao que nao pode estar certa.
func (s Schema) Check() error {
	if len(s) == 0 {
		return nil
	}

	vistos := make(map[string]bool, len(s))
	var duplicadas, semNome, semTipo, tipoInvalido []string

	for i, c := range s {
		nome := strings.TrimSpace(c.Name)
		if nome == "" {
			semNome = append(semNome, fmt.Sprintf("posição %d", i))
			continue
		}
		if vistos[nome] {
			duplicadas = append(duplicadas, nome)
		}
		vistos[nome] = true

		switch c.Type {
		case "":
			semTipo = append(semTipo, nome)
		case TypeString, TypeInt64, TypeFloat64, TypeNumeric,
			TypeBool, TypeTimestamp, TypeDate, TypeJSON, TypeBytes:
		default:
			tipoInvalido = append(tipoInvalido, fmt.Sprintf("%s (%q)", nome, c.Type))
		}
	}

	for _, p := range []struct {
		lista []string
		msg   string
	}{
		{semNome, "Schema has a column with no name at %s"},
		{duplicadas, "Schema declares %s more than once, and the second one would be ignored"},
		{semTipo, "Schema leaves %s without a Type -- a blank type would be an inference " +
			"wearing a declaration's clothes. Use sdk.TypeString, TypeInt64, TypeFloat64, " +
			"TypeNumeric, TypeBool, TypeTimestamp, TypeDate, TypeJSON or TypeBytes"},
		{tipoInvalido, "Schema uses a type that does not exist: %s"},
	} {
		if len(p.lista) > 0 {
			sort.Strings(p.lista)
			return fmt.Errorf(p.msg, strings.Join(p.lista, ", "))
		}
	}
	return nil
}
