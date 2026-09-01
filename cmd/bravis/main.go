// Command bravis e o binario unico da plataforma.
//
// Um binario com subcomandos, nao varios binarios: o plano (secao 2) descreve
// API, scheduler e workers como papeis do mesmo sistema, e um binario so mantem
// uma versao, uma imagem e um caminho de build.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zarvhq/bravis/internal/api"
	"github.com/zarvhq/bravis/internal/config"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/internal/observability"
)

func main() {
	if err := raiz().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func raiz() *cobra.Command {
	c := &cobra.Command{
		Use:           "bravis",
		Short:         "Bravis — engine de transformacao e orquestracao de dados",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.AddCommand(cmdServe(), cmdMigrate())
	return c
}

func cmdServe() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Sobe a API HTTP",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context())
		},
	}
}

func cmdMigrate() *cobra.Command {
	return &cobra.Command{
		Use:       "migrate [up|down|status]",
		Short:     "Aplica as migrations de schema",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"up", "down", "status"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return postgres.Migrate(cmd.Context(), cfg.DatabaseURL, args[0])
		},
	}
}

func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := observability.NewLogger(cfg.Env, cfg.LogLevel)

	// Encerra em SIGINT/SIGTERM. Sem isso, um deploy corta requisicoes em voo.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	srv := api.NewServer(log, map[string]api.Checker{"postgres": pool}).HTTPServer(cfg.HTTPAddr)

	erros := make(chan error, 1)
	go func() {
		log.Info("api ouvindo", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			erros <- err
		}
	}()

	select {
	case err := <-erros:
		return err
	case <-ctx.Done():
		log.Info("encerrando", "timeout", cfg.ShutdownTimeout.String())
	}

	// Contexto proprio: o de cima ja esta cancelado pelo sinal, e usa-lo aqui
	// abortaria o shutdown no mesmo instante em que ele comeca.
	ctxEnc, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(ctxEnc)
}
