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
	"time"

	"github.com/spf13/cobra"

	"github.com/zarvhq/bravis/internal/api"
	app "github.com/zarvhq/bravis/internal/application/execution"
	spec "github.com/zarvhq/bravis/internal/application/workflow"
	"github.com/zarvhq/bravis/internal/config"
	"github.com/zarvhq/bravis/internal/execution"
	"github.com/zarvhq/bravis/internal/execution/local"
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
	c.AddCommand(cmdServe(), cmdMigrate(), cmdValidate(), cmdRun())
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

// cmdRun executa um workflow na propria instancia. Sem fila, sem banco, sem
// scheduler — e o caminho curto que a emenda a secao 3 habilitou.
func cmdRun() *cobra.Command {
	var (
		workDir    string
		tentativas int
		timeout    time.Duration
	)
	c := &cobra.Command{
		Use:   "run <arquivo.yaml>",
		Short: "Executa um workflow localmente",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conteudo, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			w, err := spec.Parse(args[0], conteudo)
			if err != nil {
				return err
			}

			env := os.Getenv("BRAVIS_ENV")
			if env == "" {
				env = "local"
			}
			exec, err := local.New(env)
			if err != nil {
				return err
			}

			if workDir == "" {
				workDir = filepath.Dir(args[0])
			}
			fmt.Printf("workflow %s (%s, %d steps) em %s\n\n", w.Slug, w.Kind, len(w.Nodes), workDir)

			runner := app.Runner{
				Processo: exec,
				// Registry vazio no `run`: tasks Go sao registradas por quem
				// compila o binario, e o CLI generico nao conhece nenhuma.
				// `action:` de uma task nao registrada falha citando as
				// disponiveis, que e o comportamento util aqui.
				Go:            local.NewGoExecutor(execution.NewRegistry()),
				MaxTentativas: tentativas,
				BackoffBase:   time.Second,
				Timeout:       timeout,
				WorkDir:       workDir,
				// PATH e HOME explicitos: sem eles um `python` ou `./script.sh`
				// nao resolve. O resto do ambiente NAO e herdado, de proposito.
				Env:    map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")},
				Report: consoleReporter{},
			}
			if err := runner.Run(cmd.Context(), w); err != nil {
				return err
			}
			fmt.Printf("\nworkflow %s concluido\n", w.Slug)
			return nil
		},
	}
	c.Flags().StringVar(&workDir, "workdir", "", "diretorio de trabalho (padrao: o do arquivo)")
	c.Flags().IntVar(&tentativas, "retries", 1, "tentativas por step (1 = sem retry)")
	c.Flags().DurationVar(&timeout, "timeout", 0, "timeout por step (0 = sem limite)")
	return c
}

// consoleReporter imprime os eventos prefixados pelo step, que e o que torna a
// saida legivel quando varios rodam em paralelo no mesmo nivel.
type consoleReporter struct{}

func (consoleReporter) Evento(e execution.Event) {
	switch e.Kind {
	case execution.EventStarted:
		fmt.Printf("  ▶ %s\n", e.NodeID)
	case execution.EventLog:
		destino := os.Stdout
		if e.Stream == "stderr" {
			destino = os.Stderr
		}
		fmt.Fprintf(destino, "    %s | %s\n", e.NodeID, e.Message)
	case execution.EventSucceeded:
		fmt.Printf("  ✓ %s\n", e.NodeID)
	case execution.EventFailed:
		fmt.Printf("  ✗ %s (%s)\n", e.NodeID, e.Message)
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
