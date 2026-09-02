package config

import (
	"os"

	"github.com/AreteAcademy/bravis/internal/auth"
	"strings"
	"testing"
	"time"
)

func TestLoadExigeDatabaseURL(t *testing.T) {
	t.Setenv("BRAVIS_DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("esperava erro quando BRAVIS_DATABASE_URL falta; o processo nao pode subir sem banco")
	}
}

func TestLoadAplicaPadroes(t *testing.T) {
	t.Setenv("BRAVIS_DATABASE_URL", "postgres://u:p@localhost:5432/db")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "local" {
		t.Errorf("Env = %q, queria local", c.Env)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, queria :8080", c.HTTPAddr)
	}
	if c.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, queria 15s", c.ShutdownTimeout)
	}
}

func TestLoadRejeitaTimeoutInvalido(t *testing.T) {
	t.Setenv("BRAVIS_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("BRAVIS_SHUTDOWN_TIMEOUT_SECONDS", "quinze")

	if _, err := Load(); err == nil {
		t.Fatal("esperava erro num timeout nao numerico, em vez de cair no padrao em silencio")
	}
}

// O bug que o usuario viu: o compose tinha GOOGLE_PROJECT_ID no ambiente do
// scheduler, mas o dbt dentro da task recebia "Env var required but not
// provided". A task nao herda o ambiente — o que ela precisa e declarado.
func TestAmbienteDasTasksRepassaOQueFoiDeclarado(t *testing.T) {
	t.Setenv("GOOGLE_PROJECT_ID", "zarv-dev")
	t.Setenv("STAGE", "local")
	t.Setenv("BRAVIS_DATABASE_URL", "postgres://bravis:senha@db/bravis")

	env := AmbienteDasTasks([]string{"GOOGLE_PROJECT_ID", "STAGE"})

	if env["GOOGLE_PROJECT_ID"] != "zarv-dev" || env["STAGE"] != "local" {
		t.Errorf("nao repassou o declarado: %v", env)
	}
	if _, vazou := env["BRAVIS_DATABASE_URL"]; vazou {
		t.Error("a credencial do banco chegou na task")
	}
	if env["PATH"] == "" {
		t.Error("sem PATH nenhum comando resolve")
	}
}

// Herdar por padrao entregaria a credencial do banco a todo passo de todo
// pipeline — um workflow e um comando arbitrario escrito por outra pessoa.
func TestSemDeclaracaoSoPathEHome(t *testing.T) {
	t.Setenv("SEGREDO_QUALQUER", "nao-deve-vazar")
	env := AmbienteDasTasks(nil)
	if len(env) != 2 {
		t.Errorf("ambiente = %v, quero apenas PATH e HOME", env)
	}
}

func TestValorLiteralEVariavelAusente(t *testing.T) {
	os.Unsetenv("NAO_EXISTE")
	env := AmbienteDasTasks([]string{"STAGE=prod", "NAO_EXISTE"})

	if env["STAGE"] != "prod" {
		t.Errorf("literal nao aplicado: %v", env)
	}
	// Ausente nao vira string vazia: `GOOGLE_PROJECT_ID=""` faria o dbt falhar
	// mais tarde, com mensagem pior que a de variavel ausente.
	if _, existe := env["NAO_EXISTE"]; existe {
		t.Error("variavel ausente virou string vazia")
	}
}

// O curinga existe para quem precisa; a excecao existe porque a configuracao do
// orquestrador nunca e trabalho da task.
func TestCuringaNaoLevaAsVariaveisDoProprioBravis(t *testing.T) {
	t.Setenv("MINHA_VAR", "valor")
	t.Setenv("BRAVIS_DATABASE_URL", "postgres://bravis:senha@db/bravis")
	t.Setenv("BRAVIS_BRAND_FILE", "/etc/bravis/brand.yaml")

	env := AmbienteDasTasks([]string{"*"})

	if env["MINHA_VAR"] != "valor" {
		t.Error("curinga deveria repassar as variaveis comuns")
	}
	for k := range env {
		if strings.HasPrefix(k, "BRAVIS_") {
			t.Errorf("curinga levou %s para a task", k)
		}
	}
}

// Um campo declarado na struct mas nunca preenchido compila e passa despercebido
// — foi o que aconteceu com o webhook: a variavel estava no container, o binario
// era o novo, e o alerta simplesmente nao saia. Este teste amarra ambiente e
// campo.
func TestLoadLeOAmbienteDeCadaCampo(t *testing.T) {
	t.Setenv("BRAVIS_DATABASE_URL", "postgres://x/y")
	t.Setenv("BRAVIS_SLACK_WEBHOOK", "https://hooks.slack.com/services/abc")
	t.Setenv("BRAVIS_UI_URL", "https://bravis.zarv.net")
	t.Setenv("BRAVIS_TASK_ENV", "GOOGLE_PROJECT_ID,STAGE")
	t.Setenv("BRAVIS_POD_SERVICE_ACCOUNT", "bravis-task")
	t.Setenv("BRAVIS_POD_TOLERATIONS", "kubernetes.io/arch=arm64:NoSchedule")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SlackWebhook != "https://hooks.slack.com/services/abc" {
		t.Errorf("SlackWebhook = %q", c.SlackWebhook)
	}
	if c.UIURL != "https://bravis.zarv.net" {
		t.Errorf("UIURL = %q", c.UIURL)
	}
	if len(c.TaskEnv) != 2 {
		t.Errorf("TaskEnv = %v", c.TaskEnv)
	}
	if c.Pods.ServiceAccount != "bravis-task" {
		t.Errorf("ServiceAccount = %q", c.Pods.ServiceAccount)
	}
	if len(c.Pods.Toleracoes) != 1 || c.Pods.Toleracoes[0].Efeito != "NoSchedule" {
		t.Errorf("Toleracoes = %+v", c.Pods.Toleracoes)
	}
}

// Fora do local, subir sem credencial e recusado. A interface dispara pipeline
// que escreve no data warehouse; aberta na internet ela e um controle remoto do
// warehouse. Um aviso no log nao bastaria — ninguem le o log de um processo que
// funciona.
func TestForaDoLocalExigeCredencial(t *testing.T) {
	t.Setenv("BRAVIS_DATABASE_URL", "postgres://x/y")
	t.Setenv("BRAVIS_ENV", "prod")

	if _, err := Load(); err == nil {
		t.Fatal("BRAVIS_ENV=prod subiu sem credencial")
	}

	h, err := auth.GerarHash("senha-de-teste-longa")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAVIS_AUTH_USUARIO", "operador")
	t.Setenv("BRAVIS_AUTH_SENHA_HASH", h)
	t.Setenv("BRAVIS_AUTH_SEGREDO", "um-segredo-de-teste-com-mais-de-32-bytes")

	c, err := Load()
	if err != nil {
		t.Fatalf("com credencial completa deveria subir: %v", err)
	}
	if !c.Auth.Ativa() {
		t.Error("a credencial nao chegou na Config")
	}
}

// Em local a interface pode ficar aberta: ali o servidor escuta a maquina de
// quem desenvolve, e pedir senha a cada `make up` empurraria o time a desligar
// a autenticacao de vez.
func TestLocalSobeSemCredencial(t *testing.T) {
	t.Setenv("BRAVIS_DATABASE_URL", "postgres://x/y")
	t.Setenv("BRAVIS_ENV", "local")
	if _, err := Load(); err != nil {
		t.Fatalf("local deveria subir sem credencial: %v", err)
	}
}
