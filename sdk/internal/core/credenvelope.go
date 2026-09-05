package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// O formato guardado, e e contrato: mudar depois exige migracao, e migracao de
// credencial e a que ninguem quer fazer as pressas.
//
//	brevis-cred/1\n<nonce 12 bytes><ciphertext+tag>   AES-256-GCM
//	brevis-cred/1p\n<valor>                           em claro
//
// Os dois carregam versao na primeira linha pelo mesmo motivo: um leitor que
// nao reconhece a versao trata como ausente e cai na semente, em vez de
// falhar -- durante um rollout o mesmo store tem as duas.
const (
	formatoCifrado = "brevis-cred/1"
	formatoClaro   = "brevis-cred/1p"
)

// CredentialBox embrulha e desembrulha a credencial.
//
// Zero value grava em claro. Com chave, AES-256-GCM.
//
// A cifra e OPCIONAL porque o controle de verdade e o do storage: um bucket
// dedicado, com IAM para uma unica service account e acesso publico bloqueado,
// ja resolve quem le. Uma chave de aplicacao protegeria contra quem tem
// leitura e nao tem a chave -- mas a chave vive no mesmo secret das tasks,
// entao quem le o bucket tambem a tem. Chamar isso de seguranca seria teatro,
// e teatro e pior que a ausencia porque encerra a conversa.
//
// Num diretorio a recomendacao se inverte: diretorio e mais facil de acabar
// compartilhado do que um bucket com IAM.
type CredentialBox struct {
	gcm cipher.AEAD
}

// NewCredentialBox resolve a chave. Vazia devolve um envelope que grava em claro.
func NewCredentialBox(chave string) (CredentialBox, error) {
	if strings.TrimSpace(chave) == "" {
		return CredentialBox{}, nil
	}

	bruta, err := base64.StdEncoding.DecodeString(strings.TrimSpace(chave))
	if err != nil {
		return CredentialBox{}, fmt.Errorf("credential store: the key is not valid base64: %w", err)
	}
	if len(bruta) != 32 {
		return CredentialBox{}, fmt.Errorf("credential store: the key decodes to %d bytes, and "+
			"AES-256 needs 32. Generate one with: head -c 32 /dev/urandom | base64", len(bruta))
	}

	bloco, err := aes.NewCipher(bruta)
	if err != nil {
		return CredentialBox{}, fmt.Errorf("credential store: %w", err)
	}
	gcm, err := cipher.NewGCM(bloco)
	if err != nil {
		return CredentialBox{}, fmt.Errorf("credential store: %w", err)
	}
	return CredentialBox{gcm: gcm}, nil
}

// Encrypted diz se ha chave.
func (e CredentialBox) Encrypted() bool { return e.gcm != nil }

// Seal produz os bytes que vao para o store.
func (e CredentialBox) Seal(valor string) ([]byte, error) {
	if !e.Encrypted() {
		return append([]byte(formatoClaro+"\n"), valor...), nil
	}

	nonce := make([]byte, e.gcm.NonceSize())
	// Sorteado a cada escrita. Reusar nonce com a mesma chave em GCM quebra a
	// cifra, e e o erro mais comum de quem faz isto pela primeira vez.
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("credential store: nonce: %w", err)
	}

	out := append([]byte(formatoCifrado+"\n"), nonce...)
	return e.gcm.Seal(out, nonce, []byte(valor), []byte(formatoCifrado)), nil
}

// Open devolve o valor guardado, ou "" quando nao ha um utilizavel.
//
// Versao desconhecida, truncado, ou que nao decifra sao todos "nao ha valor":
// o chamador cai na semente e a execucao segue. Falhar aqui trocaria uma
// credencial talvez velha por nenhuma.
func (e CredentialBox) Open(bruto []byte, onde string) string {
	cabecalho, corpo, ok := primeiraLinha(bruto)
	if !ok {
		slog.Warn("credential store: ignoring a stored value with no version line",
			"store", onde, "falling_back_to", "Credential.Value")
		return ""
	}

	switch cabecalho {
	case formatoClaro:
		return string(corpo)

	case formatoCifrado:
		if !e.Encrypted() {
			slog.Warn("credential store: the stored value is encrypted and no key is set",
				"store", onde, "falling_back_to", "Credential.Value")
			return ""
		}
		n := e.gcm.NonceSize()
		if len(corpo) < n+e.gcm.Overhead() {
			slog.Warn("credential store: the stored value is truncated",
				"store", onde, "falling_back_to", "Credential.Value")
			return ""
		}
		claro, err := e.gcm.Open(nil, corpo[:n], corpo[n:], []byte(formatoCifrado))
		if err != nil {
			// Chave trocada, corrompido ou adulterado. Nao se diz qual:
			// distinguir daria a quem adultera um oraculo.
			slog.Warn("credential store: the stored value does not decrypt",
				"store", onde, "falling_back_to", "Credential.Value")
			return ""
		}
		return string(claro)

	default:
		slog.Warn("credential store: ignoring a version this build does not read",
			"store", onde, "version", cabecalho, "falling_back_to", "Credential.Value")
		return ""
	}
}

// avisos garante que o "esta em claro" saia UMA vez por store, e nao a cada
// execucao de pipeline no mesmo processo -- um aviso repetido vira ruido, e
// ruido e como um aviso deixa de ser lido.
var avisos sync.Map

// WarnIfPlaintext loga uma vez que este store grava sem cifra.
func WarnIfPlaintext(e CredentialBox, onde string) {
	if e.Encrypted() {
		return
	}
	if _, jaAvisou := avisos.LoadOrStore(onde, true); jaAvisou {
		return
	}
	slog.Info("credential store: writing in the clear",
		"store", onde,
		"why", "no key is set",
		"protection", "whatever guards the store itself -- bucket IAM, directory permissions",
		"to_encrypt", "set the store's Key, or "+EnvCredentialKey)
}

// primeiraLinha separa o cabecalho do resto.
func primeiraLinha(b []byte) (string, []byte, bool) {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i]), b[i+1:], true
		}
	}
	return "", nil, false
}
