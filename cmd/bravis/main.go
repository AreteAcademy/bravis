// Command bravis e o binario unico da plataforma.
//
// Um binario com subcomandos, nao varios binarios: o plano (secao 2) descreve
// API, scheduler e workers como papeis do mesmo sistema, e um binario so mantem
// uma versao, uma imagem e um caminho de build.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/zarvhq/bravis/internal/api"
	app "github.com/zarvhq/bravis/internal/application/execution"
	spec "github.com/zarvhq/bravis/internal/application/workflow"
	"github.com/zarvhq/bravis/internal/config"
	wfdom "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/execution"
	"github.com/zarvhq/bravis/internal/execution/local"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/internal/observability"
	"github.com/zarvhq/bravis/internal/queue"
	"github.com/zarvhq/bravis/internal/scheduler"
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
	c.AddCommand(cmdServe(), cmdMigrate(), cmdValidate(), cmdRun(), cmdPublish(), cmdScheduler(), cmdBackfill())
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

// abrir monta pool e repositorios. Repetido em tres subcomandos; um helper
// evita divergirem no tratamento de erro.
func abrir(ctx context.Context) (*postgres.Pool, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, config.Config{}, err
	}
	pool, err := postgres.New(ctx, cfg.DatabaseURL)
	return pool, cfg, err
}

func cmdPublish() *cobra.Command {
	var projeto string
	c := &cobra.Command{
		Use:   "publish <arquivo.yaml> ...",
		Short: "Publica workflows e suas agendas no banco",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pool, _, err := abrir(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			// Um projeto padrao para o modo local. A secao 4 tem Project como
			// entidade de primeira classe; ate haver gestao de projetos, este
			// slug fixo mantem a FK honesta sem inventar hierarquia.
			var idProjeto uuid.UUID
			err = pool.QueryRow(ctx, `
				INSERT INTO projects (id, slug, name) VALUES ($1, $2, $2)
				ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
				RETURNING id`, uuid.New(), projeto).Scan(&idProjeto)
			if err != nil {
				return err
			}

			repo := postgres.NewWorkflowRepo(pool)
			for _, arq := range args {
				conteudo, err := os.ReadFile(arq)
				if err != nil {
					return err
				}
				w, err := spec.Parse(arq, conteudo)
				if err != nil {
					return err
				}
				if err := repo.Publicar(ctx, w, idProjeto); err != nil {
					return err
				}
				fmt.Printf("  publicado  %-24s %s\n", w.Slug, agenda(w.Schedule))
			}
			return nil
		},
	}
	c.Flags().StringVar(&projeto, "project", "default", "slug do projeto")
	return c
}

func cmdScheduler() *cobra.Command {
	var intervalo time.Duration
	var concorrencia int
	c := &cobra.Command{
		Use:   "scheduler",
		Short: "Materializa agendas em runs e as executa",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			pool, cfg, err := abrir(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			log := observability.NewLogger(cfg.Env, cfg.LogLevel)
			runs := postgres.NewRunRepo(pool)
			fila := queue.New(pool.Pool)

			sched := scheduler.NewScheduler(
				postgres.NewScheduleRepo(pool), postgres.NewWorkflowRepo(pool), runs, fila, log,
				scheduler.OpcoesScheduler{Intervalo: intervalo})

			// O dispatcher precisa saber EXECUTAR um run. Le a definicao gravada
			// no proprio Run — o snapshot da secao 22 — e nao o YAML em disco,
			// que pode ter mudado desde o disparo.
			exec, err := local.New(cfg.Env)
			if err != nil {
				return err
			}
			executar := func(ctx context.Context, id uuid.UUID) error {
				r, err := runs.Buscar(ctx, id)
				if err != nil {
					return err
				}
				var w wfdom.Workflow
				if err := json.Unmarshal(r.Definicao, &w); err != nil {
					return err
				}
				return app.Runner{
					Processo: exec,
					Go:       local.NewGoExecutor(execution.NewRegistry()),
					Env:      map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")},
					Report:   consoleReporter{},
				}.Run(ctx, w)
			}

			disp := scheduler.New(scheduler.Config{
				Worker: "local", MaxConcorrente: concorrencia,
			}, fila, runs, executar, log)

			log.Info("scheduler e dispatcher no ar",
				"intervalo", intervalo.String(), "concorrencia", concorrencia)

			// Os dois lacos correm juntos, e independentes: o scheduler CRIA, o
			// dispatcher EXECUTA. E a separacao que a secao 37 exige — um pode
			// cair sem interromper o outro.
			erros := make(chan error, 2)
			go func() { erros <- sched.Run(ctx) }()
			go func() { erros <- disp.Run(ctx) }()

			<-ctx.Done()
			log.Info("encerrando")
			return <-erros
		},
	}
	c.Flags().DurationVar(&intervalo, "interval", 10*time.Second, "intervalo entre ciclos")
	c.Flags().IntVar(&concorrencia, "concurrency", 5, "runs simultaneos")
	return c
}

func cmdBackfill() *cobra.Command {
	var de, ate string
	c := &cobra.Command{
		Use:   "backfill <workflow>",
		Short: "Materializa slots passados de um workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			inicio, err := time.Parse("2006-01-02", de)
			if err != nil {
				return fmt.Errorf("--from: %w (use AAAA-MM-DD)", err)
			}
			fim, err := time.Parse("2006-01-02", ate)
			if err != nil {
				return fmt.Errorf("--to: %w (use AAAA-MM-DD)", err)
			}
			// Fim do dia: `--to 2026-01-31` deve incluir o dia 31 inteiro.
			fim = fim.Add(24*time.Hour - time.Second)

			pool, cfg, err := abrir(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			s := scheduler.NewScheduler(
				postgres.NewScheduleRepo(pool), postgres.NewWorkflowRepo(pool),
				postgres.NewRunRepo(pool), queue.New(pool.Pool),
				observability.NewLogger(cfg.Env, cfg.LogLevel), scheduler.OpcoesScheduler{})

			n, err := s.Backfill(ctx, args[0], inicio, fim)
			if err != nil {
				return err
			}
			fmt.Printf("  %d run(s) de backfill enfileirados para %s (%s a %s)\n",
				n, args[0], de, ate)
			fmt.Println("  rode `bravis scheduler` para executa-los")
			return nil
		},
	}
	c.Flags().StringVar(&de, "from", "", "data inicial (AAAA-MM-DD)")
	c.Flags().StringVar(&ate, "to", "", "data final (AAAA-MM-DD)")
	_ = c.MarkFlagRequired("from")
	_ = c.MarkFlagRequired("to")
	return c
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

	ui := api.NewUI(postgres.NewLeituraRepo(pool), log)
	srv := api.NewServer(log, map[string]api.Checker{"postgres": pool}, ui).HTTPServer(cfg.HTTPAddr)

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
