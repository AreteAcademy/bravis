package config

import (
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
