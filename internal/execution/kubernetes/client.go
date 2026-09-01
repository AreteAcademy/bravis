// Package kubernetes executa cada passo de um workflow como um POD proprio.
//
// A dinamica e a da secao 2 do plano, e e o que separa este motor de um worker
// monolitico: o pod sobe com a imagem EXATA do passo, roda um comando, reporta e
// morre. Um passo de dbt sobe a imagem de dbt com 1Gi; o fetcher em Go ao lado
// sobe uma imagem de 10 MB com 32Mi. Numa imagem unica os dois pagariam o maior
// dos dois — em bytes de pull, em memoria reservada e em superficie.
//
// O cliente e escrito sobre a stdlib, sem client-go. A biblioteca oficial traz
// centenas de dependencias e dezenas de MB para o que aqui sao quatro chamadas
// REST: criar pod, ler status, ler log, apagar pod. A mesma escolha ja foi feita
// para o React (bundle vendorizado) e para o CSS (Tailwind standalone): o custo
// de uma dependencia grande so se paga quando se usa uma fracao grande dela.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Caminhos que o kubelet monta em todo pod com service account.
const (
	dirSA        = "/var/run/secrets/kubernetes.io/serviceaccount"
	arquivoToken = dirSA + "/token"
	arquivoCA    = dirSA + "/ca.crt"
	arquivoNS    = dirSA + "/namespace"
)

// Cliente fala com o servidor de API.
type Cliente struct {
	base      string
	namespace string
	http      *http.Client

	// O token e lido do disco a cada uso, com cache curto. Tokens projetados
	// EXPIRAM e o kubelet os reescreve no lugar; guardar o valor no boot faz o
	// processo comecar a receber 401 depois de uma hora — falha que aparece
	// tarde e parece problema de RBAC.
	mu         sync.Mutex
	token      string
	tokenLido  time.Time
	tokenTTL   time.Duration
	lerArquivo func(string) ([]byte, error)
}

// ErrForaDoCluster e devolvido quando nao ha service account montada.
type ErrForaDoCluster struct{ Motivo string }

func (e ErrForaDoCluster) Error() string {
	return "fora de um cluster Kubernetes: " + e.Motivo
}

// NoCluster monta o cliente a partir do ambiente que o kubelet injeta.
func NoCluster() (*Cliente, error) {
	host, porta := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || porta == "" {
		return nil, ErrForaDoCluster{Motivo: "KUBERNETES_SERVICE_HOST/PORT ausentes"}
	}
	ns, err := os.ReadFile(arquivoNS)
	if err != nil {
		return nil, ErrForaDoCluster{Motivo: "namespace nao montado: " + err.Error()}
	}
	ca, err := os.ReadFile(arquivoCA)
	if err != nil {
		return nil, ErrForaDoCluster{Motivo: "CA nao montada: " + err.Error()}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("CA do cluster invalida")
	}

	transporte := http.DefaultTransport.(*http.Transport).Clone()
	transporte.TLSClientConfig = tlsConfig{pool}.build()

	return &Cliente{
		base:      fmt.Sprintf("https://%s", net_(host, porta)),
		namespace: strings.TrimSpace(string(ns)),
		// Sem timeout no cliente: o GET de log com follow fica aberto pela
		// duracao inteira da task. O corte vem do contexto de cada chamada.
		http:       &http.Client{Transport: transporte},
		tokenTTL:   time.Minute,
		lerArquivo: os.ReadFile,
	}, nil
}

// Namespace onde os pods sao criados.
func (c *Cliente) Namespace() string { return c.namespace }

func (c *Cliente) autorizar(r *http.Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || time.Since(c.tokenLido) > c.tokenTTL {
		b, err := c.lerArquivo(arquivoToken)
		if err != nil {
			return fmt.Errorf("lendo token da service account: %w", err)
		}
		c.token, c.tokenLido = strings.TrimSpace(string(b)), time.Now()
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	return nil
}

func (c *Cliente) requisicao(ctx context.Context, metodo, caminho string, corpo any) (*http.Response, error) {
	var leitor io.Reader
	if corpo != nil {
		b, err := json.Marshal(corpo)
		if err != nil {
			return nil, err
		}
		leitor = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, metodo, c.base+caminho, leitor)
	if err != nil {
		return nil, err
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.autorizar(req); err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// erroDaAPI transforma o Status do Kubernetes em erro legivel. O corpo tras o
// motivo real ("pods is forbidden: ... cannot create resource"), e descarta-lo
// deixaria so "422", que nao ajuda ninguem.
func erroDaAPI(res *http.Response) error {
	defer res.Body.Close()
	var status struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	corpo, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	_ = json.Unmarshal(corpo, &status)
	if status.Message != "" {
		return fmt.Errorf("kubernetes %s: %s", res.Status, status.Message)
	}
	return fmt.Errorf("kubernetes %s: %s", res.Status, strings.TrimSpace(string(corpo)))
}

// CriarPod cria o pod e devolve o nome atribuido.
func (c *Cliente) CriarPod(ctx context.Context, p Pod) (Pod, error) {
	res, err := c.requisicao(ctx, http.MethodPost,
		"/api/v1/namespaces/"+c.namespace+"/pods", p)
	if err != nil {
		return Pod{}, err
	}
	if res.StatusCode >= 300 {
		return Pod{}, erroDaAPI(res)
	}
	defer res.Body.Close()

	var criado Pod
	if err := json.NewDecoder(res.Body).Decode(&criado); err != nil {
		return Pod{}, fmt.Errorf("lendo pod criado: %w", err)
	}
	return criado, nil
}

// LerPod devolve o estado atual.
func (c *Cliente) LerPod(ctx context.Context, nome string) (Pod, error) {
	res, err := c.requisicao(ctx, http.MethodGet,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+nome, nil)
	if err != nil {
		return Pod{}, err
	}
	if res.StatusCode >= 300 {
		return Pod{}, erroDaAPI(res)
	}
	defer res.Body.Close()

	var p Pod
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		return Pod{}, err
	}
	return p, nil
}

// Logs abre o stream de saida do container. Com `follow`, a resposta so termina
// quando o container termina — e por isso que nao ha timeout no http.Client.
func (c *Cliente) Logs(ctx context.Context, nome string, seguir bool) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("container", nomeContainer)
	if seguir {
		q.Set("follow", "true")
	}
	res, err := c.requisicao(ctx, http.MethodGet,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+nome+"/log?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, erroDaAPI(res)
	}
	return res.Body, nil
}

// ApagarPod remove o pod.
func (c *Cliente) ApagarPod(ctx context.Context, nome string) error {
	res, err := c.requisicao(ctx, http.MethodDelete,
		"/api/v1/namespaces/"+c.namespace+"/pods/"+nome, nil)
	if err != nil {
		return err
	}
	// 404 e sucesso para quem apaga: o objetivo era o pod nao existir mais.
	if res.StatusCode >= 300 && res.StatusCode != http.StatusNotFound {
		return erroDaAPI(res)
	}
	res.Body.Close()
	return nil
}
