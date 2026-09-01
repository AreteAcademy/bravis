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
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zarvhq/bravis/internal/api"
	spec "github.com/zarvhq/bravis/internal/application/workflow"
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
	c.AddCommand(cmdServe(), cmdMigrate(), cmdValidate())
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

// cmdValidate existe para dar retorno ANTES de publicar. A secao 5 do plano
// manda validar a DAG antes de salvar; poder rodar isso no editor ou na CI, sem
// banco e sem servidor, e o que torna a regra util em vez de burocratica.
func cmdValidate() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <arquivo.yaml|diretorio> ...",
		Short: "Valida arquivos de workflow (nao precisa de banco)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var arquivos []string
			for _, alvo := range args {
				info, err := os.Stat(alvo)
				if err != nil {
					return err
				}
				if !info.IsDir() {
					arquivos = append(arquivos, alvo)
					continue
				}
				encontrados, err := filepath.Glob(filepath.Join(alvo, "*.y*ml"))
				if err != nil {
					return err
				}
				arquivos = append(arquivos, encontrados...)
			}
			if len(arquivos) == 0 {
				return fmt.Errorf("nenhum arquivo .yaml encontrado")
			}

			var falhas int
			for _, a := range arquivos {
				conteudo, err := os.ReadFile(a)
				if err != nil {
					fmt.Printf("  ERRO  %s: %v\n", a, err)
					falhas++
					continue
				}
				w, err := spec.Parse(a, conteudo)
				if err != nil {
					fmt.Printf("  ERRO  %v\n", err)
					falhas++
					continue
				}
				fmt.Printf("  ok    %-28s %s  %d steps, %d dependencias%s\n",
					w.Slug, w.Kind, len(w.Nodes), len(w.Edges), agenda(w.Schedule))
			}
			if falhas > 0 {
				return fmt.Errorf("%d de %d arquivo(s) com erro", falhas, len(arquivos))
			}
			return nil
		},
	}
}

func agenda(cron string) string {
	if cron == "" {
		return "  (manual)"
	}
	return "  cron " + cron
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
