package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Credential guarda a credencial rotacionada num objeto do GCS.
//
// E o que faz a renovacao valer entre execucoes: sem store, o valor renovado
// morre com o processo, e alguem recola a semente por janela para sempre. Com
// ele, a variavel de ambiente deixa de guardar o valor ROTATIVO e passa a
// guardar a semente, colada uma vez.
//
//	Refresh: &from.Refresh{
//	    URL:       "https://api.example.com/auth/session",
//	    ExpiresAt: from.JSONField("expires"),
//	    Store:     gcs.Credential{Bucket: "meu-projeto-credentials", Object: "app-session"},
//	}
//
// O objeto guarda a credencial e nada alem dela: nem `expires`, nem quem, nem
// quando -- o metadado do objeto ja diz o quando, e o resto envelhece.
//
// Importar este pacote custa o cliente do Google Storage. Um fetcher que use
// from.FileStore nunca o compila -- e a mesma regra de core.Store.
type Credential struct {
	// Bucket e Object dizem onde. Obrigatorios.
	//
	// O nome do objeto vem de voce e NUNCA da URL: URL carrega segredo em
	// query string, e nome de objeto vaza para log e listagem.
	Bucket string
	Object string

	// Key cifra o conteudo com AES-256-GCM. Opcional; vazia consulta
	// BREVIS_CREDENTIAL_KEY, e vazia nos dois grava em claro, dizendo uma vez
	// no log que esta em claro.
	//
	// Num bucket dedicado, com IAM para uma unica service account e acesso
	// publico bloqueado, a chave protege pouco: ela vive no mesmo secret das
	// tasks, entao quem le o bucket tambem a tem. O controle e o do bucket.
	Key string

	// Client e o cliente do Storage. Nil cria um por execucao a partir das
	// credenciais do ambiente -- que num pod com Workload Identity e tudo o
	// que se precisa.
	Client *storage.Client
}

// CheckStore satisfaz core.CredentialStoreChecker: recusa na montagem.
func (c Credential) CheckStore() error {
	if c.Bucket == "" || c.Object == "" {
		return fmt.Errorf("gcs.Credential needs Bucket and Object")
	}
	env, err := core.NewCredentialBox(c.chave())
	if err != nil {
		return err
	}
	core.WarnIfPlaintext(env, c.Describe())
	return nil
}

// Describe nomeia o objeto, nunca o conteudo.
func (c Credential) Describe() string { return "gs://" + c.Bucket + "/" + c.Object }

func (c Credential) chave() string {
	if c.Key != "" {
		return c.Key
	}
	return os.Getenv(core.EnvCredentialKey)
}

// geracoes lembra a geracao lida por objeto, para a escrita condicional.
//
// Vive no processo porque e exatamente esse o escopo do que se lembra: "a
// geracao que EU li". A coordenacao entre processos e o proprio
// ifGenerationMatch, no servidor.
var geracoes sync.Map

// Load devolve a credencial guardada, e lembra a geracao para o Save.
//
// Objeto ausente e "nao ha valor", nao erro: e a primeira execucao de todas, e
// o chamador cai na semente.
func (c Credential) Load() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, fechar, err := c.cliente(ctx)
	if err != nil {
		return "", err
	}
	defer fechar()

	obj := cli.Bucket(c.Bucket).Object(c.Object)
	r, err := obj.NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		// Zero significa "nao existia quando eu li", e o Save vira
		// DoesNotExist -- que e a condicao certa para a primeira gravacao.
		geracoes.Store(c.Describe(), int64(0))
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("credential store: reading %s: %w", c.Describe(), err)
	}
	defer func() { _ = r.Close() }()

	bruto, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", fmt.Errorf("credential store: reading %s: %w", c.Describe(), err)
	}
	geracoes.Store(c.Describe(), r.Attrs.Generation)

	env, err := core.NewCredentialBox(c.chave())
	if err != nil {
		return "", err
	}
	return env.Open(bruto, c.Describe()), nil
}

// Save grava a credencial, condicionado a geracao que o Load leu.
//
// Se outro processo escreveu no meio, o GCS devolve 412 e a gravacao NAO
// acontece -- em vez de sobrescrever. Isso e compare-and-swap de verdade, sem
// lock, e e a razao de este store existir em vez de um arquivo num volume:
// `rename` num gcsfuse nao e atomico, e ali so caberia ultimo-vence.
//
// Perder o 412 nao e erro. O outro processo renovou tambem, o valor dele
// tambem vale, e o desta execucao continua servindo ate o fim dela. O que nao
// pode acontecer e o mais velho chegar por ultimo e apagar o mais novo.
func (c Credential) Save(valor string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, fechar, err := c.cliente(ctx)
	if err != nil {
		return err
	}
	defer fechar()

	env, err := core.NewCredentialBox(c.chave())
	if err != nil {
		return err
	}
	conteudo, err := env.Seal(valor)
	if err != nil {
		return err
	}

	cond := storage.Conditions{DoesNotExist: true}
	if g, ok := geracoes.Load(c.Describe()); ok {
		if gen := g.(int64); gen != 0 {
			cond = storage.Conditions{GenerationMatch: gen}
		}
	}

	w := cli.Bucket(c.Bucket).Object(c.Object).If(cond).NewWriter(ctx)
	// Sem cache: um objeto de credencial servido de cache seria uma versao
	// velha lida como se fosse a atual.
	w.CacheControl = "no-store"
	if _, err := w.Write(conteudo); err != nil {
		_ = w.Close()
		return c.erroDeEscrita(err)
	}
	if err := w.Close(); err != nil {
		return c.erroDeEscrita(err)
	}
	return nil
}

// erroDeEscrita transforma o conflito de geracao em "nao gravei, e esta tudo
// bem", e deixa o resto ser erro.
func (c Credential) erroDeEscrita(err error) error {
	var api *googleapi.Error
	if errors.As(err, &api) && (api.Code == http.StatusPreconditionFailed || api.Code == http.StatusConflict) {
		slog.Info("credential store: another process rotated first, keeping theirs",
			"store", c.Describe(),
			"why", "the write was conditional on the generation this run read")
		return nil
	}
	return fmt.Errorf("credential store: writing %s: %w", c.Describe(), err)
}

// cliente devolve o cliente e como solta-lo. Um cliente que o chamador passou
// e do chamador, e nao se fecha aqui.
func (c Credential) cliente(ctx context.Context) (*storage.Client, func(), error) {
	if c.Client != nil {
		return c.Client, func() {}, nil
	}
	cli, err := storage.NewClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("credential store: google storage client: %w", err)
	}
	return cli, func() { _ = cli.Close() }, nil
}
