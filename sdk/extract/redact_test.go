package extract

import (
	"strings"
	"testing"
)

// A URL redigida aparece em toda linha de log do extract e em toda mensagem de
// erro. Um vazamento aqui é uma credencial viva num agregador de logs que
// muita gente lê.
func TestRedactNaoDeixaSegredoPassar(t *testing.T) {
	casos := []string{
		"https://api.exemplo.com/v1?key=SEGREDO",
		"https://api.exemplo.com/v1?api_key=SEGREDO",
		"https://api.exemplo.com/v1?API_KEY=SEGREDO",
		"https://api.exemplo.com/v1?apiKey=SEGREDO",
		"https://api.exemplo.com/v1?apikey=SEGREDO",
		"https://api.exemplo.com/v1?access_token=SEGREDO",
		"https://api.exemplo.com/v1?Token=SEGREDO",
		"https://api.exemplo.com/v1?refresh-token=SEGREDO",
		"https://api.exemplo.com/v1?client_secret=SEGREDO",
		"https://api.exemplo.com/v1?secret=SEGREDO",
		"https://api.exemplo.com/v1?signature=SEGREDO",
		"https://api.exemplo.com/v1?sig=SEGREDO",
		"https://api.exemplo.com/v1?password=SEGREDO",
		"https://api.exemplo.com/v1?pwd=SEGREDO",
		"https://api.exemplo.com/v1?auth=SEGREDO",
		"https://api.exemplo.com/v1?X-Api-Key=SEGREDO",
		"https://api.exemplo.com/v1?sessionId=SEGREDO",
		"https://api.exemplo.com/v1?credentials=SEGREDO",
		// A pior de todas: a senha no userinfo, que o url.String imprime
		// inteira.
		"https://usuario:SEGREDO@api.exemplo.com/v1",
		// E combinada, para não passar por acidente numa só.
		"https://usuario:SEGREDO@api.exemplo.com/v1?token=SEGREDO&latitude=-23.5",
	}

	for _, c := range casos {
		got := redactURL(c)
		if strings.Contains(got, "SEGREDO") {
			t.Errorf("vazou:\n  %s\n  -> %s", c, got)
		}
	}
}

// Redigir demais não pode apagar o que serve para depurar: a URL tem de
// continuar reconhecível.
func TestRedactPreservaOQueNaoESegredo(t *testing.T) {
	got := redactURL("https://api.open-meteo.com/v1/forecast?latitude=-23.55&longitude=-46.63&api_key=X")

	for _, quer := range []string{"api.open-meteo.com", "/v1/forecast", "latitude=-23.55", "longitude=-46.63"} {
		if !strings.Contains(got, quer) {
			t.Errorf("a URL perdeu %q, e assim não serve para depurar: %s", quer, got)
		}
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("a chave não foi redigida: %s", got)
	}
}

// Um usuário sem senha não é segredo, e apagá-lo tiraria informação útil.
func TestRedactMantemUsuarioSemSenha(t *testing.T) {
	got := redactURL("https://usuario@api.exemplo.com/v1")
	if !strings.Contains(got, "usuario") {
		t.Errorf("o usuário sem senha sumiu: %s", got)
	}
}

func TestRedactUrlInvalidaNaoEntraEmPanico(t *testing.T) {
	if got := redactURL("://isto-nao-e-url"); got != "[invalid url]" {
		t.Errorf("redactURL(inválida) = %q", got)
	}
}

// A redação erra para o lado seguro: "monkey" contém "key" e vira ***. É o
// preço de não depender de adivinhar o nome que o fornecedor escolheu.
func TestRedactErraParaOLadoSeguro(t *testing.T) {
	if got := redactURL("https://x/y?monkey=1"); !strings.Contains(got, "REDACTED") {
		t.Errorf("esperado sobre-redigir: %s", got)
	}
}
