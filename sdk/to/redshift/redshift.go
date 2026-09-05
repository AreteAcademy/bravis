// Package redshift writes records into Amazon Redshift.
//
// # O que este driver NAO tem, e por que voce precisa saber
//
// Nao existe imagem do Redshift para rodar local. Entao, diferente de todo
// outro destino deste SDK, ele sai com **verificacao parcial**:
//
//	testado sem cluster   a geracao do SQL (COPY e MERGE), como funcao pura,
//	                      e a escrita do arquivo de staging no S3
//	NAO testado           que o cluster aceita esse SQL
//
// Isto esta aqui, e nao num rodape, porque e a informacao que muda a decisao
// de quem vai usar. O que da para testar sem cluster e exatamente o que o
// mergeSQL e o reconcile do BigQuery ja provaram valer: SQL montado dentro de
// um metodo com cliente nunca tinha sido visto por um teste, e foi assim que a
// v0.12.0 saiu com casamento posicional.
package redshift

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Table carrega registros numa tabela do Redshift, via COPY a partir do S3.
//
//	To: redshift.Table{
//	    DSN:     os.Getenv("RS_DSN"),
//	    Name:    "landing.pedidos",
//	    Staging: "s3://meu-bucket/stage/",
//	    IAMRole: "arn:aws:iam::123456789012:role/redshift-copy",
//	    Store:   s3.New(cliente),
//	}
//
// INSERT linha a linha no Redshift e inviavel: e um banco colunar, e cada
// INSERT paga o custo de um bloco. A carga certa e COPY a partir do S3, que e
// por que o driver de arquivos vem antes no roadmap -- a camada de staging e a
// mesma.
type Table struct {
	// DSN e a conexao ao cluster, no dialeto do Postgres. Obrigatoria.
	DSN string

	// Name e a tabela, com esquema. Obrigatoria.
	Name string

	// Staging e o prefixo em S3 onde o lote e escrito antes do COPY.
	// Obrigatorio: nao ha caminho inline no Redshift.
	Staging string

	// IAMRole e o papel que o cluster assume para ler o S3. Obrigatorio.
	//
	// E role, e nao chave de acesso, de proposito: uma chave na URL do COPY
	// acaba no log de query do cluster, que muita gente le. Este driver nao
	// aceita chave -- se voce precisa de uma, o lugar dela e a role.
	IAMRole string

	// Store escreve o arquivo de staging. Obrigatorio; use store/s3.
	Store core.Store

	// KeepStagedFile deixa o arquivo no S3 depois do COPY, para inspecao.
	KeepStagedFile bool

	// Executor roda o SQL no cluster. Nil abre uma conexao pelo DSN.
	//
	// Existe para que a geracao do SQL seja testavel sem cluster, que e a
	// unica parte deste driver que da para testar sem cluster.
	Executor SQLExecutor
}

// SQLExecutor e o minimo que este driver precisa de uma conexao.
type SQLExecutor interface {
	Exec(ctx context.Context, sql string) error
}

// Describe satisfaz core.Writer. Nomeia a tabela, nunca o DSN nem a role.
func (t Table) Describe() string { return "redshift:" + t.Name }

// Write satisfaz core.Writer.
func (t Table) Write(ctx context.Context, envelopes []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	res := &core.LoadResult{Dedup: opt.Dedup, Strategy: "copy", Format: string(core.FormatNDJSON)}
	if opt.Dedup == "" {
		res.Dedup = core.DedupNone
	}
	inicio := time.Now()
	falhar := func(err error) (*core.LoadResult, error) {
		res.Duration = time.Since(inicio)
		return res, err
	}

	if err := t.checar(); err != nil {
		return falhar(err)
	}
	if len(envelopes) == 0 {
		return falhar(nil)
	}
	if err := core.CheckColumns(opt.Columns, envelopes); err != nil {
		return falhar(err)
	}

	// As colunas vem da declaracao, e nao do lote: no Redshift o SDK nao le o
	// esquema antes de carregar, entao a declaracao E o contrato -- e ela ja
	// foi conferida contra o lote inteiro pelo CheckColumns.
	colunas := opt.Columns
	if len(colunas) == 0 {
		colunas = camposDe(envelopes)
	}

	dados, err := EncodeNDJSON(envelopes, colunas)
	if err != nil {
		return falhar(err)
	}
	res.BytesStaged = int64(len(dados))

	local, err := core.ParseLocation(t.Staging)
	if err != nil {
		return falhar(fmt.Errorf("redshift: Staging: %w", err))
	}
	chave := strings.TrimSuffix(local.Prefix, "/") + "/" +
		fmt.Sprintf("brevis-%d.ndjson", time.Now().UnixNano())
	chave = strings.TrimPrefix(chave, "/")

	if err := t.Store.Create(ctx, local.Bucket, chave, bytes.NewReader(dados)); err != nil {
		return falhar(fmt.Errorf("redshift: staging to s3://%s/%s: %w", local.Bucket, chave, err))
	}
	uri := "s3://" + local.Bucket + "/" + chave

	if !t.KeepStagedFile {
		defer func() {
			// O arquivo de staging fica se a limpeza falhar: perder a carga
			// por causa de um DELETE seria trocar um problema pequeno por um
			// grande. O aviso e o que diz que ele ficou.
			if err := t.apagar(ctx, local.Bucket, chave); err != nil {
				avisarSobra(ctx, uri, err)
			}
		}()
	}

	exec, fechar, err := t.executor(ctx)
	if err != nil {
		return falhar(err)
	}
	defer fechar()

	comandos := []string{CopySQL(t.destinoDoCopy(res.Dedup), uri, t.IAMRole)}
	if res.Dedup == core.DedupMerge {
		comandos = append([]string{StagingTableSQL(t.Name, tempName)},
			comandos...)
		comandos = append(comandos, MergeSQL(t.Name, tempName, colunas), DropSQL(tempName))
	}

	for _, sql := range comandos {
		if err := exec.Exec(ctx, sql); err != nil {
			return falhar(fmt.Errorf("redshift: %w", err))
		}
	}

	res.RowsLoaded = int64(len(envelopes))
	return falhar(nil)
}

const tempName = "brevis_stage"

func (t Table) destinoDoCopy(d core.Dedup) string {
	if d == core.DedupMerge {
		return tempName
	}
	return t.Name
}

func (t Table) checar() error {
	faltando := []string{}
	if t.DSN == "" && t.Executor == nil {
		faltando = append(faltando, "DSN")
	}
	if t.Name == "" {
		faltando = append(faltando, "Name")
	}
	if t.Staging == "" {
		faltando = append(faltando, "Staging")
	}
	if t.IAMRole == "" {
		faltando = append(faltando, "IAMRole")
	}
	if t.Store == nil {
		faltando = append(faltando, "Store")
	}
	if len(faltando) > 0 {
		return fmt.Errorf("redshift.Table needs %s. There is no inline path on Redshift: "+
			"the batch goes to S3 and the cluster COPYs it, which is why Staging and IAMRole "+
			"are not optional", strings.Join(faltando, ", "))
	}
	if strings.Contains(t.IAMRole, "aws_access_key_id") ||
		strings.Contains(t.IAMRole, "ACCESS_KEY") {
		return fmt.Errorf("redshift.Table.IAMRole looks like an access key. This driver takes " +
			"a role ARN only: a key in the COPY statement lands in the cluster's query log, " +
			"which many people can read")
	}
	return nil
}

// EncodeNDJSON serializa o lote no formato que o COPY le.
//
// Exportada para ser testavel sem cluster, e escrita com um unico buffer que
// cresce: um lote de centenas de milhares de linhas nao pode alocar um buffer
// por registro.
func EncodeNDJSON(envelopes []core.Envelope, colunas []string) ([]byte, error) {
	var buf bytes.Buffer
	// Estimativa grosseira, so para evitar as primeiras dobras.
	buf.Grow(len(envelopes) * 128)

	// As chaves sao as mesmas em toda linha, entao sao serializadas UMA vez.
	// Passar um map[string]any ao json.Encoder por registro custava cinco
	// alocacoes por linha -- o encoder ordena as chaves e caixa cada valor,
	// e nada disso muda entre registros.
	chaves := make([][]byte, len(colunas))
	for i, c := range colunas {
		b, err := json.Marshal(c)
		if err != nil {
			return nil, fmt.Errorf("redshift: column %q: %w", c, err)
		}
		chaves[i] = append(b, ':')
	}

	enc := json.NewEncoder(&buf)
	// O COPY le JSON, nao HTML: escapar < e > so aumentaria o arquivo.
	enc.SetEscapeHTML(false)

	for i, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("redshift: row %d: %w", i+1, err)
		}

		buf.WriteByte('{')
		primeiro := true
		for j, c := range colunas {
			v, tem := obj[c]
			if !tem {
				// Coluna ausente nao vira null: com FORMAT AS JSON 'auto' a
				// ausencia deixa a coluna NULL, e escrever null explicito
				// custaria bytes sem mudar nada.
				continue
			}
			if !primeiro {
				buf.WriteByte(',')
			}
			primeiro = false
			buf.Write(chaves[j])
			if !escreverEscalar(&buf, v) {
				// Composto: o encoder resolve, e paga uma alocacao.
				if err := enc.Encode(v); err != nil {
					return nil, fmt.Errorf("redshift: row %d, column %q: %w", i+1, c, err)
				}
				// O Encode termina em \n, que aqui e separador de LINHA e nao
				// pode aparecer no meio do objeto.
				buf.Truncate(buf.Len() - 1)
			}
		}
		buf.WriteString("}\n")
	}
	return buf.Bytes(), nil
}

// CopySQL monta o COPY.
//
// FORMAT AS JSON 'auto' casa por NOME de campo, que e o oposto do que o
// INSERT ROW do BigQuery faz -- e e por isso que aqui nao ha o risco que
// custou a v0.12.0. O que ha e a role: uma chave de acesso nesta string
// acabaria no log de query do cluster.
func CopySQL(destino, uri, role string) string {
	return fmt.Sprintf("COPY %s FROM '%s' IAM_ROLE '%s' FORMAT AS JSON 'auto' TIMEFORMAT 'auto'",
		destino, uri, role)
}

// StagingTableSQL cria a temporaria com a MESMA forma do destino.
//
// LIKE, e nao uma lista de colunas escrita a mao: a temporaria que nao
// acompanha o destino e a que faz o MERGE falhar meses depois, quando alguem
// acrescenta uma coluna.
func StagingTableSQL(destino, temp string) string {
	return fmt.Sprintf("CREATE TEMP TABLE %s (LIKE %s)", temp, destino)
}

// MergeSQL monta o MERGE da dedup, com a lista de colunas NOMEADA.
//
// Nomeada sempre, e o comentario existe porque a alternativa ja aconteceu: o
// `INSERT ROW` do BigQuery casa por POSICAO, e a v0.12.0 saiu com as colunas
// trocadas de lugar porque ninguem tinha visto o SQL gerado.
func MergeSQL(destino, origem string, colunas []string) string {
	nomes := make([]string, len(colunas))
	valores := make([]string, len(colunas))
	for i, c := range colunas {
		nomes[i] = citar(c)
		valores[i] = origem + "." + citar(c)
	}
	return fmt.Sprintf(
		"MERGE INTO %s USING %s ON %s.%s = %s.%s "+
			"WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
		destino, origem,
		destino, citar(core.MetadataID), origem, citar(core.MetadataID),
		strings.Join(nomes, ", "), strings.Join(valores, ", "))
}

// DropSQL apaga a temporaria.
func DropSQL(temp string) string { return "DROP TABLE IF EXISTS " + temp }

func citar(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func camposDe(envelopes []core.Envelope) []string {
	vistos := map[string]bool{}
	for _, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			continue
		}
		for k := range obj {
			vistos[k] = true
		}
	}
	out := make([]string, 0, len(vistos))
	for k := range vistos {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
