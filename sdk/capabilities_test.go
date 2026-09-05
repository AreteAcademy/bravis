package sdk_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
	"github.com/AreteAcademy/brevis/sdk/to/mysql"
	"github.com/AreteAcademy/brevis/sdk/to/postgres"
	"github.com/AreteAcademy/brevis/sdk/to/redshift"
)

// A matriz de compatibilidade, como TESTE e não como tabela num arquivo .md.
//
// O §5 do plano dos drivers nomeia o risco: nove drivers com Metadata, Dedup,
// CreateTable e Preview são 36 combinações, e prometer as 36 sem medir é como
// o `DeleteAfterLoad` chegou à documentação com um default que ele não tinha.
//
// O que este arquivo impede é a terceira resposta. Para cada combinação só há
// duas aceitáveis:
//
//	suportado   o driver faz
//	recusado    o driver diz que não faz, nomeando o campo
//
// A que não pode existir é "aceita e ignora": uma flag escrita que não faz
// nada é a classe de defeito que este projeto mais encontrou em si mesmo.

// suporte declara o que cada destino faz com cada opção. É esta tabela que a
// documentação copia -- e é ela que o teste abaixo confere contra o código.
type suporte struct {
	dedup       bool
	createTable bool
}

var destinos = map[string]struct {
	escritor sdk.Writer
	suporte  suporte
	// pistaDaRecusa é o que a mensagem precisa conter quando o driver recusa,
	// para que quem lê saiba o que fazer.
	pistaDaRecusa string
}{
	"to.Files": {
		escritor:      to.Files{Path: "/tmp/brevis-cap"},
		suporte:       suporte{dedup: false, createTable: false},
		pistaDaRecusa: "Dedup",
	},
	"postgres.Table": {
		escritor: postgres.Table{DSN: "postgres://x/y", Name: "t"},
		suporte:  suporte{dedup: true, createTable: false},
	},
	"mysql.Table": {
		escritor: mysql.Table{DSN: "u@tcp(x)/y", Name: "t"},
		suporte:  suporte{dedup: true, createTable: false},
	},
	"redshift.Table": {
		escritor: redshift.Table{DSN: "postgres://x/y", Name: "t"},
		suporte:  suporte{dedup: true, createTable: false},
	},
	"bigquery.Table": {
		escritor: bigquery.Table{Project: "p", Dataset: "d", Name: "t"},
		suporte:  suporte{dedup: true, createTable: true},
	},
}

// TestDedupOuFazOuRecusa: nenhum destino pode aceitar Dedup e não deduplicar.
func TestDedupOuFazOuRecusa(t *testing.T) {
	for nome, d := range destinos {
		t.Run(nome, func(t *testing.T) {
			_, err := d.escritor.Write(context.Background(),
				[]sdk.Envelope{{Payload: map[string]any{"ingestion_id": "x"}}},
				sdk.WriteOptions{Dedup: sdk.DedupMerge})

			if !d.suporte.dedup {
				if err == nil {
					t.Fatalf("%s aceitou Dedup sem suportá-lo -- uma flag escrita "+
						"que não faz nada é pior que um erro", nome)
				}
				if d.pistaDaRecusa != "" && !strings.Contains(err.Error(), d.pistaDaRecusa) {
					t.Errorf("a recusa não nomeia %q: %v", d.pistaDaRecusa, err)
				}
				return
			}

			// Suporta: o erro que vier tem de ser de conexão, e não uma recusa
			// da opção. Um driver que suporta Dedup mas o rejeita na validação
			// seria a mesma mentira ao contrário.
			if err != nil && strings.Contains(err.Error(), "does not have") &&
				strings.Contains(err.Error(), "Dedup") {
				t.Errorf("%s declara suportar Dedup e o recusou: %v", nome, err)
			}
		})
	}
}

// TestNenhumDestinoSQLInventaTipo é a §4 do plano: Postgres, MySQL e Redshift
// não têm um serviço que infira tipo, e adivinhar NUMERIC(18,2) de um número
// JSON é a única coisa que este SDK decidiu não fazer.
//
// Nenhum dos três tem campo CreateTable -- e é isso que este teste fixa. Um
// campo que existisse e não criasse seria exatamente a flag morta.
func TestNenhumDestinoSQLInventaTipo(t *testing.T) {
	for nome, d := range destinos {
		if d.suporte.createTable {
			continue
		}
		t.Run(nome, func(t *testing.T) {
			if temCampo(d.escritor, "CreateTable") {
				t.Errorf("%s tem campo CreateTable e a matriz diz que não cria tabela; "+
					"ou o campo faz algo, ou ele não devia existir", nome)
			}
		})
	}
}

// TestTodoDestinoSeDescreveSemSegredo: Describe vai para log, para o Result e
// para mensagem de erro, e os DSNs carregam senha.
func TestTodoDestinoSeDescreveSemSegredo(t *testing.T) {
	segredos := []string{"senha", "secret", "password", "@tcp", "://x/y"}
	for nome, d := range destinos {
		t.Run(nome, func(t *testing.T) {
			desc := d.escritor.Describe()
			if desc == "" {
				t.Fatal("Describe vazio")
			}
			for _, s := range segredos {
				if strings.Contains(desc, s) {
					t.Errorf("Describe() = %q, e carrega %q", desc, s)
				}
			}
		})
	}
}

// temCampo diz se o tipo do driver declara o campo.
func temCampo(w sdk.Writer, nome string) bool {
	t := reflect.TypeOf(w)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	_, tem := t.FieldByName(nome)
	return tem
}
