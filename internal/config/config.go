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
	"os"
	"strconv"
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
}

// Load monta a Config e falha no boot se algo obrigatorio faltar. Falhar cedo e
// deliberado: um processo que sobe sem DATABASE_URL so descobre o problema no
// primeiro request, e ai o readiness ja mentiu para o orquestrador.
func Load() (Config, error) {
	c := Config{
		Env:             get("BRAVIS_ENV", "local"),
		HTTPAddr:        get("BRAVIS_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("BRAVIS_DATABASE_URL"),
		LogLevel:        get("BRAVIS_LOG_LEVEL", "info"),
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
	return c, nil
}

func get(chave, padrao string) string {
	if v := os.Getenv(chave); v != "" {
		return v
	}
	return padrao
}
