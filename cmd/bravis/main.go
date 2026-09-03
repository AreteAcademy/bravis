// Command bravis e o binario unico da plataforma.
//
// Um binario com subcomandos, nao varios binarios: o plano (secao 2) descreve
// API, scheduler e workers como papeis do mesmo sistema, e um binario so mantem
// uma versao, uma imagem e um caminho de build.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/AreteAcademy/bravis/internal/api"
	app "github.com/AreteAcademy/bravis/internal/application/execution"
	spec "github.com/AreteAcademy/bravis/internal/application/workflow"
	"github.com/AreteAcademy/bravis/internal/auth"
	"github.com/AreteAcademy/bravis/internal/branding"
	"github.com/AreteAcademy/bravis/internal/config"
	wfdom "github.com/AreteAcademy/bravis/internal/domain/workflow"
	"github.com/AreteAcademy/bravis/internal/execution"
	k8s "github.com/AreteAcademy/bravis/internal/execution/kubernetes"
	"github.com/AreteAcademy/bravis/internal/execution/local"
	"github.com/AreteAcademy/bravis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/bravis/internal/notify"
	"github.com/AreteAcademy/bravis/internal/observability"
	"github.com/AreteAcademy/bravis/internal/queue"
	"github.com/AreteAcademy/bravis/internal/scheduler"
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
	c.AddCommand(cmdServe(), cmdMigrate(), cmdValidate(), cmdMarca(), cmdHash(), cmdRun(), cmdPublish(),
		cmdScheduler(), cmdBackfill(), cmdVersion())
	return c
}

// Versao e carimbada no build (-ldflags). "dev" e o valor de quem compilou
// direto com `go build`, e distinguir isso de um artefato de release importa
// quando alguem reporta um comportamento estranho.
var (
	Versao = "dev"
	Commit = ""
	Data   = ""
)

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Mostra a versao do binario",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Printf("bravis %s\n", Versao)
			if Commit != "" {
				fmt.Printf("  commit  %s\n", Commit)
			}
			if Data != "" {
				fmt.Printf("  build   %s\n", Data)
			}
			fmt.Printf("  go      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
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
// emLinha imprime os params numa ordem estavel — dois runs iguais tem de
// produzir o mesmo log.
func emLinha(m map[string]string) string {
	chaves := make([]string, 0, len(m))
	for k := range m {
		chaves = append(chaves, k)
	}
	sort.Strings(chaves)
	partes := make([]string, len(chaves))
	for i, k := range chaves {
		partes[i] = k + "=" + m[k]
	}
	return strings.Join(partes, " ")
}

// paramsDaLinha traduz `--param chave=valor` repetido num mapa.
//
// Entrada sem `=` e ERRO e nao aviso: `--param load_full` (esquecendo o valor)
// rodaria com o padrao, e o operador acharia que o backfill aconteceu.
func paramsDaLinha(entradas []string) (map[string]string, error) {
	if len(entradas) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entradas))
	for _, e := range entradas {
		chave, valor, ok := strings.Cut(e, "=")
		if !ok || strings.TrimSpace(chave) == "" {
			return nil, fmt.Errorf("--param %q: use chave=valor", e)
		}
		out[strings.TrimSpace(chave)] = valor
	}
	return out, nil
}

// expandir resolve arquivos e diretorios numa lista de YAMLs, em ordem estavel.
// A ordem importa: publicar duas vezes a mesma pasta tem de produzir o mesmo log,
// senao a diferenca entre dois deploys vira ruido.
func expandir(alvos []string) ([]string, error) {
	var arquivos []string
	for _, alvo := range alvos {
		info, err := os.Stat(alvo)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			arquivos = append(arquivos, alvo)
			continue
		}
		encontrados, err := filepath.Glob(filepath.Join(alvo, "*.y*ml"))
		if err != nil {
			return nil, err
		}
		arquivos = append(arquivos, encontrados...)
	}
	if len(arquivos) == 0 {
		return nil, fmt.Errorf("nenhum arquivo .yaml encontrado em %v", alvos)
	}
	sort.Strings(arquivos)
	return arquivos, nil
}

func cmdValidate() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <arquivo.yaml|diretorio> ...",
		Short: "Valida arquivos de workflow (nao precisa de banco)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			arquivos, err := expandir(args)
			if err != nil {
				return err
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

// cmdHash gera o hash de senha que vai para a configuracao.
//
// A senha e lida do terminal, nao de um argumento: argumento aparece no `ps` de
// qualquer processo da maquina e fica gravado no historico do shell.
func cmdHash() *cobra.Command {
	return &cobra.Command{
		Use:   "hash",
		Short: "Gera o hash de BRAVIS_AUTH_SENHA_HASH (le a senha do terminal)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Fprint(os.Stderr, "senha: ")
			senha, err := lerSenha()
			if err != nil {
				return err
			}
			if len(senha) < 12 {
				return fmt.Errorf("senha curta demais (%d caracteres); "+
					"use ao menos 12 — este e o unico acesso ao painel", len(senha))
			}
			h, err := auth.GerarHash(senha)
			if err != nil {
				return err
			}
			// O hash vai para stdout sozinho, para poder ser redirecionado; os
			// rotulos vao para stderr.
			fmt.Fprintln(os.Stderr, "\nBRAVIS_AUTH_SENHA_HASH:")
			fmt.Println(h)
			fmt.Fprintln(os.Stderr, "\nFalta ainda BRAVIS_AUTH_USUARIO e um "+
				"BRAVIS_AUTH_SEGREDO de 32+ bytes (openssl rand -base64 48).")
			return nil
		},
	}
}

// lerSenha le uma linha sem eco quando ha terminal, e da entrada padrao quando
// nao ha — o segundo caso e o de um script de provisionamento.
func lerSenha() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		// Entrada redirecionada: sem terminal para desligar o eco.
		linha, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(linha, "\r\n"), err
	}
	return semEco()
}

// cmdMarca valida um arquivo de marca sem subir o servidor.
//
// Existe pelo mesmo motivo do `validate`: hoje um hexadecimal errado no
// brand.yaml so aparece quando o container sobe, e a mensagem chega pelo log do
// pod — longe de quem editou o arquivo. A CI da instalacao chama isto e o erro
// volta no pull request.
func cmdMarca() *cobra.Command {
	return &cobra.Command{
		Use:   "marca <brand.yaml>",
		Short: "Valida um arquivo de marca (nao precisa de banco)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// Carregar trata ausencia como "usa o padrao", que e certo no boot
			// e errado aqui: quem pediu para validar um caminho espera saber
			// que ele nao existe.
			if _, err := os.Stat(args[0]); err != nil {
				return err
			}
			m, err := branding.Carregar(args[0])
			if err != nil {
				return err
			}
			logo := m.Logo
			if logo == branding.LogoPadrao {
				logo += "  (simbolo embutido)"
			}
			fmt.Printf("  ok    %s · %s\n", m.Titulo, m.Subtitulo)
			fmt.Printf("        logo      %s\n", logo)
			fmt.Printf("        destaque  %s\n", m.Tema.Destaque)
			fmt.Printf("        %s\n", branding.Atribuicao)
			return nil
		},
	}
}

// cmdRun executa um workflow na propria instancia. Sem fila, sem banco, sem
// scheduler — e o caminho curto que a emenda a secao 3 habilitou.
func cmdRun() *cobra.Command {
	var paramsCrus []string
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
			informados, err := paramsDaLinha(paramsCrus)
			if err != nil {
				return err
			}
			valores, err := w.Resolver(informados)
			if err != nil {
				return err
			}

			fmt.Printf("workflow %s (%s, %d steps) em %s\n", w.Slug, w.Kind, len(w.Nodes), workDir)
			if len(valores) > 0 {
				fmt.Printf("  params: %s\n\n", emLinha(valores))
			}

			runner := app.Runner{
				Params:   valores,
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
				// PATH e HOME sempre; o resto so o que BRAVIS_TASK_ENV nomear.
				// Herdar o ambiente entregaria a credencial do banco a todo
				// passo de todo pipeline.
				Env:    config.AmbienteDasTasks(config.TaskEnvDoAmbiente()),
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
	c.Flags().StringArrayVar(&paramsCrus, "param", nil,
		"valor de um parametro declarado no workflow (chave=valor; repetivel)")
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
	var podar bool
	c := &cobra.Command{
		Use:   "publish <arquivo.yaml|diretorio> ...",
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

			// Aceita diretorio como o `validate` ja aceitava: a instalacao monta
			// uma PASTA de workflows, e obrigar o chamador a expandir o glob
			// deixaria o comando refem do shell de quem chama.
			arquivos, err := expandir(args)
			if err != nil {
				return err
			}

			repo := postgres.NewWorkflowRepo(pool)
			publicados := make([]string, 0, len(arquivos))
			for _, arq := range arquivos {
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
				publicados = append(publicados, w.Slug)
			}

			if podar {
				removidos, err := repo.Podar(ctx, idProjeto, publicados)
				if err != nil {
					return err
				}
				for _, slug := range removidos {
					fmt.Printf("  removido   %-24s (nao esta mais na pasta)\n", slug)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&projeto, "project", "default", "slug do projeto")
	// Opcional e nao padrao: `publish um-arquivo.yaml` nao pode apagar os outros
	// 48 do projeto so porque nao foram citados na linha de comando.
	c.Flags().BoolVar(&podar, "prune", false,
		"remove do projeto os workflows ausentes da lista publicada (o historico e preservado)")
	return c
}

// executorDePods decide entre pod e processo, uma vez, no boot.
//
// `auto` e o padrao porque o mesmo binario roda nos dois lugares: no laptop nao
// ha service account montada e ele cai para processo local; no cluster ha, e ele
// passa a criar pods. `on` existe para o deploy que NAO pode silenciosamente
// virar execucao local — ali, ficar sem cluster tem de ser erro de boot.
func executorDePods(cfg config.Config, log *slog.Logger) (execution.Executor, error) {
	if cfg.Pods.Modo == "off" {
		return nil, nil
	}

	cliente, err := k8s.NoCluster()
	if err != nil {
		var fora k8s.ErrForaDoCluster
		if errors.As(err, &fora) && cfg.Pods.Modo == "auto" {
			log.Info("sem cluster: passos com `image:` vao rodar na propria instancia",
				"motivo", fora.Motivo)
			return nil, nil
		}
		return nil, fmt.Errorf("BRAVIS_PODS=%s: %w", cfg.Pods.Modo, err)
	}

	ns := cfg.Pods.Namespace
	if ns == "" {
		ns = cliente.Namespace()
	}
	log.Info("executando passos como pods", "namespace", ns,
		"service_account", cfg.Pods.ServiceAccount)

	return k8s.NewExecutor(cliente, k8s.Opcoes{
		Namespace:         ns,
		ServiceAccount:    cfg.Pods.ServiceAccount,
		PullSecrets:       cfg.Pods.PullSecrets,
		EnvFromSecrets:    cfg.Pods.EnvFromSecrets,
		EnvFromConfigMaps: cfg.Pods.EnvFromConfigMaps,
		NodeSelector:      cfg.Pods.NodeSelector,
		Tolerations:       toleracoesDoPod(cfg.Pods.Toleracoes),
		ManterPodEmFalha:  cfg.Pods.ManterEmFalha,
	}), nil
}

// toleracoesDoPod traduz a configuracao para o objeto do Kubernetes. `Equal` e
// o unico operador aceito: `Exists` toleraria QUALQUER taint com aquela chave,
// que e amplo demais para uma decisao vinda de variavel de ambiente.
func toleracoesDoPod(cfg []config.Toleracao) []k8s.Toleracao {
	var out []k8s.Toleracao
	for _, t := range cfg {
		out = append(out, k8s.Toleracao{
			Key: t.Chave, Operator: "Equal", Value: t.Valor, Effect: t.Efeito,
		})
	}
	return out
}

func cmdScheduler() *cobra.Command {
	var intervalo time.Duration
	var concorrencia int
	var maxPods int
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
			// O executor de processo e OPCIONAL fora do modo local. Em cluster
			// todo passo tem `image:` e vira pod; exigir o executor local aqui
			// fazia o scheduler recusar o boot em prod com "ProcessExecutor so
			// opera com BRAVIS_ENV=local" — um guarda escrito para `bravis run`
			// que nunca deveria ter valido para este caminho.
			//
			// A variavel e do tipo da INTERFACE, e nao do ponteiro concreto:
			// atribuir um `*ProcessExecutor` nil a uma interface produz uma
			// interface NAO-nil, e o runner chamaria metodo em ponteiro nulo em
			// vez de reportar "nenhum executor de processo configurado".
			var processo execution.Executor
			if exec, err := local.New(cfg.Env); err == nil {
				processo = exec
			} else {
				var fora local.ErrForaDoLocal
				if !errors.As(err, &fora) {
					return err
				}
				// Passo sem `image:` falha citando isso, e so ele — nao o
				// scheduler inteiro.
				log.Info("sem executor de processo; todo passo precisa de `image:`")
			}
			// Executor de pods: em cluster, cada passo com `image:` vira um pod
			// proprio. Fora do cluster, `pods` fica nulo e tudo roda em processo
			// local — o MESMO YAML nos dois casos.
			pods, err := executorDePods(cfg, log)
			if err != nil {
				return err
			}

			// Resolvido uma vez, no boot: o ambiente do processo nao muda, e
			// relê-lo por run so multiplicaria chamadas ao sistema.
			// O teto de PODS. Compartilhado por todos os runs deste processo:
			// com dez passos prontos e cinco vagas, cinco correm e os demais
			// entram conforme as vagas se abrem.
			//
			// Separado de --concurrency de proposito: aquele conta RUNS, este
			// conta PASSOS. Cinco runs com tres passos paralelos cada dariam
			// quinze pods se o unico limite fosse o de runs.
			vagas := make(chan struct{}, maxPods)

			ambienteDasTasks := config.AmbienteDasTasks(cfg.TaskEnv)
			// Em modo pod o ambiente da task vem dos Secrets do cluster
			// (BRAVIS_POD_ENV_FROM_SECRETS), nao daqui. Avisar mesmo assim
			// mandava o operador procurar um problema que nao existe.
			if len(ambienteDasTasks) <= 2 && pods == nil {
				// So PATH e HOME. Um `dbt` aqui falha com "Env var required but
				// not provided", que nao aponta para a causa — dizer isto no
				// boot poupa a investigacao.
				log.Warn("tasks recebem apenas PATH e HOME",
					"dica", "declare o que elas precisam em BRAVIS_TASK_ENV (ex.: GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE)")
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
					// Os params do RUN, nao do workflow: e o snapshot da
					// entrada daquela execucao. Entram no comando por template
					// e no ambiente do passo, para que um fetcher que use o SDK
					// os enxergue sem receber nada por argumento.
					Params:   r.Params,
					Processo: processo,
					Pods:     pods,
					Go:       local.NewGoExecutor(execution.NewRegistry()),
					Env:      ambienteDasTasks,
					Report:   consoleReporter{},
					// Sem isto a tabela `task_runs` fica vazia e a DAG na tela
					// nao tem estado por passo — era a divida aberta na PHASE 2.
					Persist: runs,
					RunID:   id,
					// A tentativa DO RUN entra no nome do pod. Sem ela, o retry
					// do dispatcher recomeca o run do zero — passo na tentativa
					// 0 de novo — e reencontra o pod da tentativa anterior, que
					// pode estar preso em Pending para sempre.
					TentativaDoRun: r.Attempt,
					Vagas:          vagas,

					// O que o passo nao tem como saber e o engine tem. Vai
					// para o ambiente dele como BRAVIS_RUN_*, e o SDK usa para
					// decidir, entre outras coisas, se cria a tabela de
					// destino na primeira execucao.
					//
					// Sem Historico a resposta e sempre "nao e a primeira":
					// criar tabela sem certeza e pior que nao criar.
					Historico:   runs,
					Trigger:     r.TriggerType,
					LogicalDate: r.LogicalDate,
				}.Run(ctx, w)
			}

			disp := scheduler.New(scheduler.Config{
				Worker: "local", MaxConcorrente: concorrencia,
			}, fila, runs, executar, log)
			if cfg.SlackWebhook != "" {
				disp.Alertas = notify.NovoSlack(cfg.SlackWebhook, cfg.Env)
				disp.URLBase = cfg.UIURL
				log.Info("alerta de falha ativo", "destino", "slack")
			} else {
				// Dito no boot, uma vez: uma instalacao que falha em silencio
				// costuma ser descoberta pelo cliente, nao pelo time.
				log.Warn("sem BRAVIS_SLACK_WEBHOOK: falhas nao serao avisadas")
			}

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
	c.Flags().IntVar(&maxPods, "max-pods", 5,
		"passos simultaneos no total (em Kubernetes, o teto de pods do cluster)")
	return c
}

func cmdBackfill() *cobra.Command {
	var paramsCrus []string
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

			informados, err := paramsDaLinha(paramsCrus)
			if err != nil {
				return err
			}
			n, err := s.Backfill(ctx, args[0], inicio, fim, informados)
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
	// O caso de uso central do backfill: "reprocessa janeiro inteiro com
	// load_full=true". Os valores valem para todos os slots do intervalo.
	c.Flags().StringArrayVar(&paramsCrus, "param", nil,
		"valor de um parametro do workflow (chave=valor; repetivel)")
	_ = c.MarkFlagRequired("from")
	_ = c.MarkFlagRequired("to")
	return c
}

// acoesDaUI liga os dois efeitos da tela — pausar agenda e executar agora — aos
// componentes que ja os implementam. Existe para que a interface `api.Acoes`
// fique pequena: a UI nao deve poder fazer mais nada no sistema.
type acoesDaUI struct {
	agendas *postgres.ScheduleRepo
	sched   *scheduler.Scheduler
}

func (a acoesDaUI) Alternar(ctx context.Context, slug string) (bool, error) {
	return a.agendas.Alternar(ctx, slug)
}

func (a acoesDaUI) Disparar(ctx context.Context, slug string, agora time.Time,
	params map[string]string) (uuid.UUID, error) {
	return a.sched.Disparar(ctx, slug, agora, params)
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

	// A UI nao ganha caminho proprio para criar Run: ela chama o MESMO
	// scheduler, para que a regra da secao 37 ("o scheduler cria runs") continue
	// tendo um dono so. Aqui ele e usado sem o laco — nenhuma agenda e
	// materializada por este processo, so o disparo manual.
	agendas := postgres.NewScheduleRepo(pool)
	runsRepo := postgres.NewRunRepo(pool)
	sched := scheduler.NewScheduler(agendas, postgres.NewWorkflowRepo(pool), runsRepo,
		queue.New(pool.Pool), log, scheduler.OpcoesScheduler{})

	// A identidade visual e opcional: sem arquivo, a instalacao usa a padrao.
	// Um erro AQUI e de conteudo (cor invalida, YAML quebrado) e nao impede a
	// interface de subir — derrubar a API por causa de uma cor seria pior que
	// servi-la com o tema padrao e um aviso no log.
	marca, err := branding.Carregar(cfg.BrandFile)
	if err != nil {
		log.Warn("identidade visual ignorada", "arquivo", cfg.BrandFile, "erro", err)
	} else if marca.Titulo != branding.Padrao().Titulo {
		log.Info("identidade visual carregada", "arquivo", cfg.BrandFile, "titulo", marca.Titulo)
	}

	ui := api.NewUI(postgres.NewLeituraRepo(pool), postgres.NewWorkflowRepo(pool),
		runsRepo, acoesDaUI{agendas: agendas, sched: sched}, marca, log)
	// `inseguro` acompanha o ambiente: em local o servidor escuta http puro, e
	// um cookie Secure nunca voltaria — o login pareceria nao funcionar.
	srv := api.NewServerAutenticado(log, map[string]api.Checker{"postgres": pool}, ui,
		cfg.Auth, cfg.Env == "local").HTTPServer(cfg.HTTPAddr)
	if cfg.Auth.Ativa() {
		log.Info("interface protegida", "usuario", cfg.Auth.Usuario)
	} else {
		log.Warn("interface ABERTA: qualquer um dispara workflow",
			"dica", "defina BRAVIS_AUTH_USUARIO e BRAVIS_AUTH_SENHA_HASH")
	}

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
