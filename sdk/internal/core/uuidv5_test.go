package core

import (
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

// TestUUIDv5ConcordaComOPacote é a rede que permite reimplementar a fórmula
// congelada.
//
// Uma divergência de um bit aqui mudaria todo ingestion_id já gravado -- uma
// carga anterior deixaria de casar com uma nova, e ninguém notaria até as
// duplicatas aparecerem. Então a afirmação não é "o meu está certo": é "o meu
// é idêntico ao do pacote uuid", sobre entradas que ninguém escolheu a dedo.
func TestUUIDv5ConcordaComOPacote(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	for i := 0; i < 5000; i++ {
		dados := make([]byte, r.Intn(400))
		for j := range dados {
			dados[j] = byte(r.Intn(256))
		}

		var espaco uuid.UUID
		for j := range espaco {
			espaco[j] = byte(r.Intn(256))
		}

		quero := uuid.NewSHA1(espaco, dados)
		got := uuidV5(espaco, dados)
		if got != quero {
			t.Fatalf("divergiu em %d bytes de dados:\n  meu  %s\n  uuid %s", len(dados), got, quero)
		}
	}
}

// TestUUIDv5NoNamespaceReal cobre o caso que a produção usa, incluindo a
// chave vazia e uma bem maior que o buffer de pilha.
func TestUUIDv5NoNamespaceReal(t *testing.T) {
	entradas := [][]byte{
		nil,
		[]byte(""),
		[]byte("open_meteo|hourly|123|2026-09-05T12:00:00Z"),
		make([]byte, 1000),
	}
	for _, dados := range entradas {
		if got, quero := uuidV5(namespaceDeIngestao, dados), uuid.NewSHA1(namespaceDeIngestao, dados); got != quero {
			t.Errorf("%d bytes: meu %s, uuid %s", len(dados), got, quero)
		}
	}
}

// TestFormatarUUIDConcordaComOString: o formato canônico também é contrato --
// ele vai para a coluna.
func TestFormatarUUIDConcordaComOString(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		var u uuid.UUID
		for j := range u {
			u[j] = byte(r.Intn(256))
		}
		if got, quero := formatarUUID(u), u.String(); got != quero {
			t.Fatalf("meu %q, String() %q", got, quero)
		}
	}
}

// TestChaveMaiorQueOBufferDePilha: a chave de 192 bytes cabe na pilha, e uma
// maior cai no heap -- as duas têm de produzir o mesmo id.
func TestChaveMaiorQueOBufferDePilha(t *testing.T) {
	longo := ""
	for len(longo) < 300 {
		longo += "provedor-com-nome-comprido-"
	}

	got, err := ComputeIngestionID(longo, "entidade", "chave", "2026-09-05T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	quero := uuid.NewSHA1(namespaceDeIngestao,
		[]byte(longo+"|entidade|chave|2026-09-05T12:00:00Z")).String()
	if got != quero {
		t.Errorf("chave longa divergiu:\n  meu  %s\n  uuid %s", got, quero)
	}
}
