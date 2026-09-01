// Package config carrega e valida a configuracao do processo a partir do
// ambiente.
//
// Fica fora da arvore descrita na secao 36 do plano, que nao previu um pacote
// para isso. A alternativa seria espalhar os os.Getenv por cmd/ e
// infrastructure/; um ponto unico de leitura e validacao vale o desvio, e a
// regra 7 pede que decisoes assim sejam explicitas em vez de silenciosas.
package config

import (
	"fmt"
	"github.com/zarvhq/bravis/internal/auth"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config e o estado imutavel derivado do ambiente. Tudo que o processo precisa
// para subir esta aqui — nada le o ambiente depois do boot.
type Config struct {
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        string
	ShutdownTimeout time.Duration

	// BrandFile aponta o YAML de identidade visual. Opcional: sem ele a
	// interface usa a identidade padrao.
	BrandFile string

	// TaskEnv lista o que o processo repassa para as tasks locais. Ver
	// AmbienteDasTasks — o padrao NAO herda o ambiente, e isso e deliberado.
	TaskEnv []string

	// SlackWebhook recebe o alerta de falha definitiva. Vazio = ninguem e
	// avisado. Vem do ambiente, e nunca do YAML: quem tem a URL posta no canal
	// como se fosse a plataforma.
	SlackWebhook string

	// Auth e a credencial de operador que fecha a interface. Ver internal/auth.
	Auth auth.Credencial

	// UIURL monta o link da execucao no alerta. Sem ela o alerta diz o que
	// falhou, mas obriga quem le a procurar a run na mao.
	UIURL string

	// Pods parametriza a execucao em Kubernetes. Sao decisoes da INSTALACAO —
	// com que identidade e credenciais os pods sobem —, e por isso vem do
	// ambiente e nao do YAML do workflow: um pipeline nao deve poder escolher a
	// service account com que roda.
	Pods PodsConfig
}

type PodsConfig struct {
	// Modo: auto (usa pods quando ha cluster), on (exige) ou off (nunca).
	Modo              string
	Namespace         string
	ServiceAccount    string
	PullSecrets       []string
	EnvFromSecrets    []string
	EnvFromConfigMaps []string
	NodeSelector      map[string]string
	// Toleracoes no formato "chave=valor:efeito", separadas por virgula. O pool
	// arm64 da Zarv tem taint, e sem toleracao o pod da task fica Pending para
	// sempre — sem erro, so parado.
	Toleracoes    []Toleracao
	ManterEmFalha bool
}

// Toleracao espelha o campo do pod, sem importar o tipo do Kubernetes.
type Toleracao struct {
	Chave  string
	Valor  string
	Efeito string
}

// Load monta a Config e falha no boot se algo obrigatorio faltar. Falhar cedo e
// deliberado: um processo que sobe sem DATABASE_URL so descobre o problema no
// primeiro request, e ai o readiness ja mentiu para o orquestrador.
func Load() (Config, error) {
	c := Config{
		Env:          get("BRAVIS_ENV", "local"),
		HTTPAddr:     get("BRAVIS_HTTP_ADDR", ":8080"),
		DatabaseURL:  os.Getenv("BRAVIS_DATABASE_URL"),
		LogLevel:     get("BRAVIS_LOG_LEVEL", "info"),
		BrandFile:    get("BRAVIS_BRAND_FILE", "brand.yaml"),
		TaskEnv:      lista("BRAVIS_TASK_ENV"),
		SlackWebhook: os.Getenv("BRAVIS_SLACK_WEBHOOK"),
		UIURL:        os.Getenv("BRAVIS_UI_URL"),
		Auth: auth.Credencial{
			Usuario: os.Getenv("BRAVIS_AUTH_USUARIO"),
			Hash:    os.Getenv("BRAVIS_AUTH_SENHA_HASH"),
			Segredo: []byte(os.Getenv("BRAVIS_AUTH_SEGREDO")),
		},
		Pods: PodsConfig{
			Modo:              get("BRAVIS_PODS", "auto"),
			Namespace:         os.Getenv("BRAVIS_POD_NAMESPACE"),
			ServiceAccount:    os.Getenv("BRAVIS_POD_SERVICE_ACCOUNT"),
			PullSecrets:       lista("BRAVIS_POD_PULL_SECRETS"),
			EnvFromSecrets:    lista("BRAVIS_POD_ENV_FROM_SECRETS"),
			EnvFromConfigMaps: lista("BRAVIS_POD_ENV_FROM_CONFIGMAPS"),
			NodeSelector:      pares("BRAVIS_POD_NODE_SELECTOR"),
			Toleracoes:        toleracoes("BRAVIS_POD_TOLERATIONS"),
			ManterEmFalha:     os.Getenv("BRAVIS_POD_MANTER_EM_FALHA") == "true",
		},
		ShutdownTimeout: 15 * time.Second,
	}

	if v := os.Getenv("BRAVIS_SHUTDOWN_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("BRAVIS_SHUTDOWN_TIMEOUT_SECONDS: %q nao e um inteiro", v)
		}
		c.ShutdownTimeout = time.Duration(n) * time.Second
	}

	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("BRAVIS_DATABASE_URL e obrigatoria")
	}
	switch c.Pods.Modo {
	case "auto", "on", "off":
	default:
		return Config{}, fmt.Errorf("BRAVIS_PODS: %q invalido (auto, on ou off)", c.Pods.Modo)
	}
	if err := c.Auth.Validar(); err != nil {
		return Config{}, err
	}
	// Fora do local, subir sem credencial e recusado.
	//
	// A interface dispara pipeline: um POST em /workflows/<slug>/trigger roda um
	// `dbt build` que escreve no data warehouse. Aberta na internet, ela e um
	// controle remoto do warehouse para qualquer pessoa — foi exatamente o
	// estado em que o ambiente de dev subiu, e ninguem percebeu porque nada
	// falhava. Um aviso no log nao teria bastado: ninguem le o log de um
	// processo que funciona. Falhar no boot e o que torna o descuido visivel.
	//
	// `local` fica de fora porque ali o servidor escuta a maquina de quem
	// desenvolve, e exigir senha a cada `make up` empurraria o time a desligar
	// a autenticacao de vez.
	if c.Env != "local" && !c.Auth.Ativa() {
		return Config{}, fmt.Errorf(
			"BRAVIS_ENV=%s exige credencial: defina BRAVIS_AUTH_USUARIO, "+
				"BRAVIS_AUTH_SENHA_HASH (gere com `bravis hash`) e "+
				"BRAVIS_AUTH_SEGREDO", c.Env)
	}
	return c, nil
}

// AmbienteDasTasks monta o ambiente que cada passo local recebe.
//
// A task NAO herda o ambiente do orquestrador. A razao e concreta: o processo do
// Bravis carrega BRAVIS_DATABASE_URL com usuario e senha do Postgres, e um
// workflow e um comando arbitrario escrito por outra pessoa — herdar por padrao
// entregaria a credencial do banco a todo passo de todo pipeline.
//
// O que a task precisa, entao, e declarado: `BRAVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE`
// repassa essas duas do ambiente do processo. `NOME=valor` define um literal.
// `*` repassa tudo MENOS as BRAVIS_* — o curinga existe para quem precisa, e a
// excecao existe porque a configuracao do orquestrador nunca e trabalho da task.
//
// PATH e HOME entram sempre: sem PATH nenhum comando resolve, e o erro seria um
// "not found" que nao explica nada.
// Funcao de pacote e nao metodo: `bravis run` roda sem banco e por isso sem
// Config — mas precisa do mesmo ambiente.
func AmbienteDasTasks(nomes []string) map[string]string {
	env := map[string]string{
		"PATH": os.Getenv("PATH"),
		"HOME": os.Getenv("HOME"),
	}
	for _, entrada := range nomes {
		if entrada == "*" {
			for _, kv := range os.Environ() {
				k, v, _ := strings.Cut(kv, "=")
				if strings.HasPrefix(k, "BRAVIS_") {
					continue
				}
				env[k] = v
			}
			continue
		}
		if nome, valor, ok := strings.Cut(entrada, "="); ok {
			env[nome] = valor
			continue
		}
		// Nome sem valor: repassa se existir. Ausente NAO vira string vazia —
		// `GOOGLE_PROJECT_ID=""` faria o dbt falhar mais tarde, com uma
		// mensagem pior do que a de variavel ausente.
		if v, existe := os.LookupEnv(entrada); existe {
			env[entrada] = v
		}
	}
	return env
}

// toleracoes le "chave=valor:efeito,outra=valor:efeito".
//
// Entrada malformada e IGNORADA em vez de virar erro de boot: uma toleracao
// errada deixa o pod Pending, que e visivel; recusar o boot do scheduler por
// causa dela pararia tambem os workflows que nao precisam daquele pool.
func toleracoes(chave string) []Toleracao {
	var out []Toleracao
	for _, entrada := range lista(chave) {
		par, efeito, temEfeito := strings.Cut(entrada, ":")
		k, v, temValor := strings.Cut(par, "=")
		if !temValor || !temEfeito {
			continue
		}
		out = append(out, Toleracao{Chave: strings.TrimSpace(k),
			Valor: strings.TrimSpace(v), Efeito: strings.TrimSpace(efeito)})
	}
	return out
}

// TaskEnvDoAmbiente le BRAVIS_TASK_ENV para quem nao carregou a Config inteira.
func TaskEnvDoAmbiente() []string { return lista("BRAVIS_TASK_ENV") }

// lista separa por virgula, ignorando vazios — "a,,b" e um erro de digitacao, e
// um nome de secret vazio faria o pod inteiro ser recusado pelo servidor.
func lista(chave string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(chave), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pares le "chave=valor,outra=valor" — o formato de nodeSelector.
func pares(chave string) map[string]string {
	out := map[string]string{}
	for _, p := range lista(chave) {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func get(chave, padrao string) string {
	if v := os.Getenv(chave); v != "" {
		return v
	}
	return padrao
}
