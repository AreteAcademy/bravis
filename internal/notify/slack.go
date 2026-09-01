// Package notify avisa quando uma execucao falha.
//
// Existe porque era a maior ausencia frente ao Kestra: os 51 flows daquele
// repositorio tinham, cada um, o MESMO bloco `errors: alert_slack` copiado —
// vinte linhas de payload repetidas cinquenta vezes. Aqui o alerta e uma
// propriedade da INSTALACAO: configura-se o webhook uma vez e todo workflow
// passa a avisar, com opcao de silenciar um.
//
// O webhook nao vem do YAML do workflow de proposito. Ele e uma credencial: quem
// tiver acesso a URL posta no canal como se fosse a plataforma, e um arquivo de
// pipeline nao e lugar para isso.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Alerta e o que se conta sobre uma falha.
type Alerta struct {
	Workflow    string
	RunID       string
	Status      string
	Trigger     string
	Tentativas  int
	LogicalDate *time.Time
	Erro        string

	// Passo e o node que falhou. Vem como campo proprio, e nao so embutido no
	// texto do erro, porque e a primeira coisa que quem esta de plantao procura:
	// "qual passo?" antes de "por que?".
	Passo string

	// TrechoDoLog sao as ultimas linhas da saida daquele passo, lidas de
	// `task_runs.log`. Sem isto o alerta diz que algo falhou; com isto ele diz o
	// que falhou e por que, sem ninguem precisar abrir a tela as 4h.
	TrechoDoLog string

	// Tags do workflow viram os campos "Dominio" e "Pipeline" da mensagem — no
	// Kestra isso vinha de `labels`, e e o que faz o alerta ser acionavel sem
	// abrir a tela.
	Tags []string

	// URLBase da UI, para o link direto da execucao. Vazia = sem link.
	URLBase string
}

// Notificador manda o alerta. Interface pequena para que o dispatcher nao
// conheca Slack — e para que o teste nao precise de rede.
type Notificador interface {
	Falhou(ctx context.Context, a Alerta) error
}

// Slack posta num Incoming Webhook.
type Slack struct {
	Webhook string
	Cliente *http.Client

	// Ambiente aparece no cabecalho ("prod", "dev"). Sem isso, um alerta de
	// homologacao as tres da manha e indistinguivel de um de producao.
	Ambiente string
}

func NovoSlack(webhook, ambiente string) *Slack {
	return &Slack{
		Webhook:  webhook,
		Ambiente: ambiente,
		// Timeout curto: avisar e importante, mas travar o dispatcher esperando
		// o Slack seria trocar um incidente por outro.
		Cliente: &http.Client{Timeout: 5 * time.Second},
	}
}

// Falhou posta a mensagem.
func (s *Slack) Falhou(ctx context.Context, a Alerta) error {
	if s.Webhook == "" {
		return nil
	}
	corpo, err := json.Marshal(s.mensagem(a))
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Webhook, bytes.NewReader(corpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := s.Cliente.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		// O Slack responde texto puro ("invalid_payload", "no_service"), nao
		// JSON. Repassar o corpo e o que permite distinguir webhook revogado de
		// payload malformado sem abrir o navegador.
		motivo, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("slack respondeu %s: %s", res.Status, strings.TrimSpace(string(motivo)))
	}
	return nil
}

type bloco map[string]any

func (s *Slack) mensagem(a Alerta) map[string]any {
	dominio, pipeline := s.classificar(a)

	campos := []bloco{
		campo("*Domínio:*\n`" + dominio + "`"),
		campo("*Pipeline:*\n`" + pipeline + "`"),
		campo("*Status:*\n:x: " + strings.ToUpper(a.Status)),
		campo("*Origem:*\n`" + a.Trigger + "`"),
	}
	if a.Passo != "" {
		campos = append(campos, campo("*Passo:*\n`"+a.Passo+"`"))
	}
	if a.Tentativas > 0 {
		campos = append(campos, campo(fmt.Sprintf("*Tentativas:*\n%d", a.Tentativas)))
	}
	if a.LogicalDate != nil {
		campos = append(campos, campo("*Data lógica:*\n"+a.LogicalDate.Local().Format("02/01/2006 15:04")))
	}

	blocos := []bloco{
		{"type": "header", "text": bloco{
			"type": "plain_text", "emoji": true,
			"text": ":rotating_light: Falha no pipeline" + s.sufixoDeAmbiente(),
		}},
		{"type": "section", "fields": campos},
	}

	if a.Erro != "" {
		// A mensagem de erro ja carrega as ultimas linhas de stderr; cortar em
		// 900 caracteres evita o limite de 3000 do bloco do Slack, que faria a
		// mensagem inteira ser recusada em vez de truncada.
		blocos = append(blocos, bloco{"type": "section", "text": bloco{
			"type": "mrkdwn", "text": "```" + truncar(a.Erro, 900) + "```",
		}})
	}
	// O log entra DEPOIS do erro e separado dele: o erro e a conclusao, o log e
	// a evidencia. Juntos num bloco so, o Slack corta os dois no mesmo limite e
	// costuma sobrar a evidencia sem a conclusao.
	if a.TrechoDoLog != "" {
		blocos = append(blocos, bloco{"type": "section", "text": bloco{
			"type": "mrkdwn", "text": "*Últimas linhas:*\n```" + truncar(a.TrechoDoLog, 900) + "```",
		}})
	}
	if a.URLBase != "" && a.RunID != "" {
		url := strings.TrimRight(a.URLBase, "/") + "/runs/" + a.RunID
		blocos = append(blocos, bloco{"type": "context", "elements": []bloco{
			{"type": "mrkdwn", "text": "<" + url + "|abrir a execução> · `" + a.RunID + "`"},
		}})
	}

	return map[string]any{
		// `text` fora dos blocos e o que aparece na notificacao do celular e na
		// lista de canais. Sem ele o Slack mostra "This content can't be
		// displayed" no preview.
		"text":   fmt.Sprintf(":rotating_light: %s falhou", a.Workflow),
		"blocks": blocos,
	}
}

// classificar deriva dominio e pipeline das tags, com o slug como reserva.
//
// A convencao segue a do repositorio de dados: a primeira tag e o produto
// ("zarv"), a segunda e o dominio ("id", "platform"). Sem tags, o slug ja diz o
// suficiente para nao deixar o alerta anonimo.
func (s *Slack) classificar(a Alerta) (dominio, pipeline string) {
	pipeline = a.Workflow
	dominio = "-"
	for _, t := range a.Tags {
		if t != "zarv" && t != "dbt" && t != "python" {
			dominio = t
			break
		}
	}
	if dominio == "-" {
		if partes := strings.SplitN(a.Workflow, "_", 2); len(partes) == 2 {
			dominio = partes[0]
		}
	}
	return dominio, pipeline
}

func (s *Slack) sufixoDeAmbiente() string {
	if s.Ambiente == "" || s.Ambiente == "prod" {
		return ""
	}
	return " (" + s.Ambiente + ")"
}

func campo(texto string) bloco {
	return bloco{"type": "mrkdwn", "text": texto}
}

func truncar(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… (truncado)"
}
