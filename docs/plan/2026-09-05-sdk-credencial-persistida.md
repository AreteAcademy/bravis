# A credencial rotacionada precisa sobreviver ao processo

**Escrito em** 2026-09-05 · **Base** `sdk/v0.27.2` · **Alvo** `sdk/v0.28.0`

> **EXECUTADO** em 2026-09-05. O passo 1 saiu sozinho na `sdk/v0.27.3`, como a
> spec manda; o passo 2 na `sdk/v0.29.0`. Os onze critérios estão cobertos,
> exceto o 11, que depende de dev de pé e de alguém com o cookie válido — a
> forma dele sem cluster está em `TestSegundaExecucaoUsaOQueVeioDoVolume`.
> O §"O que saiu diferente" no fim registra três desvios.

Spec executável. **São dois passos, os dois no SDK** — nada no motor, nada no
cluster, nada em `zarv-applications`.

| # | passo | por quê |
|---|---|---|
| 1 | **Consertar o §9** do [`SDK_V9.md`](../SDK_V9.md) | a renovação hoje vai sem a credencial; sem isso não há o que salvar |
| 2 | **`Refresh.Store`** | o valor renovado morre com o processo |

Pré-requisito de infraestrutura, **já feito**: o bucket
`zarv-data-pipeline-credentials` existe em `us-central1` — acesso uniforme,
acesso público bloqueado, versionamento com expurgo de versões antigas em 7 dias,
IAM apenas para `zarv-data@zarv-development-94b6`, que é a GSA que as tasks já
usam por Workload Identity.

---

## O problema, em uma frase

Alguns fornecedores não têm login programático: **um humano cola um cookie**, ele
tem expiração deslizante, e só o endpoint de renovação empurra a janela. Se o
valor renovado não sobrevive à execução, a janela nunca anda e a credencial morre
no prazo de quem a colou — e morre calada, com 401.

A troca que resolve, e é a razão da feature existir:

| hoje | depois |
|---|---|
| a env guarda o **valor rotativo** → recolar toda janela | a env guarda a **semente**, colada uma vez |
| o valor novo morre com o processo | o valor novo vai para o store |

---

## Passo 1 — a renovação vai sem a credencial

`Auth.Refresh` existe para empurrar a janela. Hoje ele não empurra, e **mata a
execução**:

```
error="refresh …/auth/session: refresh response has no field \"expires\""
```

### A causa, isolada

`AsCookie` põe a credencial no jar, semeado pela **URL da fonte**. O `cookiejar`
do Go, sem `Path` explícito no cookie, usa como padrão o **diretório da URL que o
originou**. Reproduzido com servidor local imprimindo os cabeçalhos:

| URL da fonte | URL da renovação | a renovação recebeu `Cookie`? |
|---|---|---|
| `/api/proxy/occurrences` | `/api/auth/session` | **não** |
| `/api/proxy/occurrences` | `/api/proxy/session` | sim |

Sem credencial, o endpoint responde `null`; `ExpiresAt` não acha `expires`; a
execução morre **antes da primeira página**.

### Por que o teste não pega

`TestRefreshRenovaOCookieParaAsPaginas` usa `srv.URL + "/dados"` como fonte — o
diretório dessa URL é `/`, que casa com qualquer path. **O teste passa porque a
fonte está na raiz, e nenhuma API de verdade está.**

### O conserto

Aplicar a credencial no header da requisição de renovação, em vez de depender do
jar. Não muda o comportamento das páginas.

**Prova:** o teste existente, com a fonte em `/api/v1/dados` e a renovação em
`/auth/session`. Hoje ele falha.

---

## Passo 2 — `Refresh.Store`

```go
Auth: &from.Credential{
    Value: from.FromEnv("GABRIEL_SESSION_COOKIE"),   // a semente, uma vez
    Apply: from.AsCookie,
    Refresh: &from.Refresh{
        URL:       "https://365.gabriel.com.br/api/auth/session",
        ExpiresAt: from.JSONField("expires"),
        WarnAfter: 7 * 24 * time.Hour,
        Store:     gcs.Credential{Bucket: "…-credentials", Object: "gabriel-session"},
    },
},
```

`Store` é opcional. **Ausente, o comportamento é exatamente o de hoje** — o valor
renovado vale só para a execução.

### A ordem de leitura, que é o ponto todo

1. há valor no store → usa;
2. não há, ou não lê → usa `Value`, a semente;
3. renova;
4. **grava o resultado no store.**

A semente deixa de ser o que se gerencia e passa a ser o que se usa uma vez.

### Dois stores, uma interface de duas funções

```go
gcs.Credential{Bucket, Object}      // no cluster
from.FileStore{Dir, Name}           // local, ou onde não houver objeto
```

`FileStore` resolve o diretório por `Dir` → `BREVIS_CREDENTIAL_DIR` → nada (e
desliga, dizendo que desligou). Um diretório pode ser `./.brevis`, um volume do
compose, ou um volume montado no Kubernetes — **é o mesmo `Store`**, e é o que
faz a feature servir quem não tem GCS.

`store/s3` já tem interface idêntica à do `store/gcs` (`Scheme`, `List`, `Open`,
`Create`), então **AWS entra trocando o valor**, sem redesenho.

### Concorrência: use o que o GCS dá

Dois processos renovando ao mesmo tempo gravam dois valores, e o mais velho pode
chegar por último.

No GCS há resposta exata: gravar com `ifGenerationMatch` na geração lida. Se
outro escreveu no meio, a gravação falha com 412 e o processo relê em vez de
sobrescrever — compare-and-swap, sem lock.

**Isto não vem de graça:** `store/gcs` tem `Open`/`Create`/`List` e **não** tem
escrita condicional. Precisa ser acrescentada. Se for adiada, que seja adiada
**por escrito**, porque a diferença é entre CAS de verdade e último-vence.

No `FileStore` não há equivalente: fica lock por `O_EXCL` com expiração, ou
último-vence documentado.

> Ao levar para o S3, conferir antes: escrita condicional existe lá, mas a
> semântica não é a mesma. Supor paridade é o mesmo erro de supor que
> `INSERT ROW` casa por nome.

### Cifragem: opcional, e eis o cálculo

A versão anterior desta spec exigia cifrar. **O cálculo mudou quando o volume
compartilhado saiu de cena.** Hoje o controle é o bucket: dedicado, IAM para uma
única service account, acesso público bloqueado, cifrado em repouso pelo GCS.

Uma chave de aplicação protegeria contra quem tem leitura no bucket e não tem a
chave — mas a chave viveria no mesmo secret das tasks, então quem lê o bucket
também a tem. **Isso é teatro**, e chamar de segurança o que não protege é pior
que não ter.

Então: `Key` é **campo opcional**. Sem ela, grava em claro e loga **uma vez** que
está em claro. Com ela, AES-256-GCM. Para o `FileStore` a recomendação é usar —
um diretório é mais fácil de acabar compartilhado do que um bucket com IAM.

Quando houver chave, o formato em disco fica:

```
brevis-cred/1\n                    versão, em texto, primeira linha
<nonce 12 bytes><ciphertext+tag>   AES-256-GCM
```

Versão desconhecida cai na semente em vez de falhar — durante um rollout o mesmo
store tem as duas. E o nonce é sorteado a cada escrita: reusar nonce com a mesma
chave em GCM quebra a cifra, e é o erro mais comum de quem faz isso pela primeira
vez.

### O resto que é fácil errar

- **Falha ao gravar não derruba a execução, mas grita.** A carga já aconteceu; o
  que se perdeu foi a rotação. `ERROR` no log **e** em `Result`, como o
  `CredentialExpiry` já vai — aviso que só existe no log é morte silenciosa com
  passos a mais.
- **O nome do objeto é dado pelo chamador**, nunca derivado da URL: URL carrega
  segredo em query string, e nome de objeto vaza para log e listagem.
- **A credencial nunca em log**, nem truncada, nem em `Describe()`.
- **Nada além da credencial no store.** Nem `expires`, nem `user`, nem a data —
  o metadado do objeto já diz o quando, e o resto envelhece.

---

## O que não fazer

- **Não** guardar no BigQuery, no banco do cliente, nem numa tabela do Brevis. As
  três foram descartadas: as duas primeiras por serem analíticas e do cliente, a
  terceira porque o cliente pode usar só o SDK.
- **Não** mexer no motor nem no cluster. Se esta spec exigir isso, o desenho saiu
  do trilho — o SDK já alcança o GCS por Workload Identity.
- **Não** tornar `Store` obrigatório.
- **Não** construir uma interface de store genérica agora por causa do cache
  futuro. Uma abstração com uma implementação é um palpite sobre a segunda. O que
  se deve fazer é **não impedir**: manter leitura e escrita atrás de duas
  funções, e não espalhar chamada de storage pelo caminho da renovação.

---

## Critério de pronto

**Passo 1**

1. A requisição de renovação leva a credencial, com a fonte e a renovação em
   prefixos de path diferentes.
2. O teste existente, ajustado para `/api/v1/dados` + `/auth/session`, passa —
   e falha sem o conserto.

**Passo 2**

3. `Refresh.Store` opcional; ausente, o comportamento é o de hoje.
4. Ordem de leitura store → semente → renova → grava, com teste nos três ramos.
5. `gcs.Credential` grava com `ifGenerationMatch`; 412 faz reler. Teste com duas
   gravações concorrentes. **Ou**, se adiado, está escrito no godoc que é
   último-vence e por quê.
6. `FileStore` resolve `Dir` → `BREVIS_CREDENTIAL_DIR` → desligado, e loga qual
   venceu. Arquivo `0600`, diretório `0700`.
7. `Key` opcional; sem ela, um log **único** dizendo que está em claro.
8. Falha ao gravar não derruba a execução e aparece em `Result`.
9. A credencial não aparece em log nem no nome do objeto.
10. `go test ./... -short` verde e `go vet ./...` limpo.

**A prova**

11. O `gabriel` roda duas vezes em dev com `GABRIEL_SESSION_COOKIE` **removida
    depois da primeira**. A segunda autentica com o que veio do store.
    É literalmente o que foi pedido.

---

## Como entregar isto a um agente

Os dois passos são código do SDK e cabem num agente só. **Mas entregue o passo 1
primeiro e sozinho** — um agente que receber a spec inteira vai implementar o
store sobre um `Refresh` que não funciona, e a prova final devolve exatamente o
erro que começou esta sequência.

O passo 11 depende de dev estar de pé e de alguém com o cookie válido.

---

## Anexo: por que não foi um volume

O caminho anterior desta spec ia por `PersistentVolume` + GCS Fuse. Foi
abandonado, e vale registrar por quê — a pergunta *"precisamos modificar o GKE
global?"* é o que o desfez:

| | volume (GCS Fuse) | store GCS |
|---|---|---|
| muda o cluster | **sim, addon global** | não |
| objeto de cluster | `PersistentVolume` não é namespaced | nenhum |
| escrita atômica | `rename` no gcsfuse **não** é | escrita de objeto **é** |
| concorrência | lock aproximado | `ifGenerationMatch` |
| **sobrevive a refazer a infra** | **não** — PVC morre com o cluster | **sim** |
| passos até funcionar | 7 | 2 |

O último é o mais forte, e não foi meu: é justamente numa recriação de ambiente
que ninguém lembra que havia uma credencial rotativa em algum lugar.

E o único ponto em que o volume ganharia — manter o SDK sem dependência de nuvem
— já estava resolvido pela separação em pacotes, que é a regra que o próprio SDK
estabeleceu com `to/bigquery` e `store/s3`. Eu não tinha visto que `store/gcs` já
existia.

---

## O que saiu diferente

### O passo 1 não se resolvia com o conserto que a spec descreve

A spec diz: "aplicar a credencial no header da requisição de renovação, em vez
de depender do jar". Isso conserta a **ida** — e só apareceu ao escrever a
asserção certa que não basta.

O cookie que a renovação **reemite** volta pelo jar e fica preso ao diretório da
URL de renovação. As páginas, em `/api/proxy/...`, seguem com o valor velho: a
renovação renova para ninguém, e o store gravaria um valor que nunca foi usado —
que é exatamente o "guardar lixo" do §0 da spec anterior, por outro caminho.

Então a credencial deixou de ser cookie de jar **nas duas direções**. Um
`credentialJar` desvia os nomes dela antes que o jar os guarde, o que mantém a
invariante da `v0.26.0` (cada cookie num lugar só, nenhum nome duas vezes) e dá
de brinde o que o passo 2 precisa: **o valor rotacionado fica na mão**.

A rotação também é aplicada no laço de páginas, porque uma API pode reemitir a
sessão em qualquer resposta — sem isso, uma regressão que o teste da `v0.26.0`
pegou.

### A escrita condicional não foi adiada

O critério 5 abre a porta para adiar, "por escrito". Não foi: `gcs.Credential`
grava com `ifGenerationMatch` na geração que o `Load` leu, e o 412 faz a
execução **manter o valor do outro** em vez de sobrescrever.

Perder a corrida não é erro e não vai como erro: o outro processo renovou
também, o valor dele também vale, e o desta execução serve até o fim dela. O que
não pode acontecer — e é o que o critério pede — é o mais velho chegar por
último e apagar o mais novo.

A primeira gravação usa `DoesNotExist` em vez de geração, senão duas primeiras
execuções simultâneas gravariam as duas.

### O que já existia da spec anterior, e ficou

A `v0.28.0` foi entregue contra a spec do volume, e três coisas dela
sobreviveram porque esta spec as mantém:

- **`FileStore`** — esta spec o lista como um dos dois stores, e o diretório
  pode ser um volume montado. Ele não é sobra do desenho antigo.
- **A cifragem** virou **opcional**, que é a mudança desta spec. Antes o store
  recusava a ligar sem chave; agora grava em claro e avisa **uma vez**, com o
  aviso deduplicado por store para não virar ruído a cada pipeline.
- **`PodSpec.Volumes` no motor** ficou. Não é órfão: é o que faz
  `BREVIS_CREDENTIAL_DIR` funcionar num pod para quem escolher `FileStore`. Mas
  a recomendação do `docs/KUBERNETES.md` passou a ser o `gcs.Credential`, com o
  volume como alternativa — e o anexo desta spec diz por quê melhor do que eu
  diria.

### Uma coisa a conferir antes do S3

A spec já avisa, e vale repetir onde alguém vá olhar: a escrita condicional
existe no S3, mas a semântica **não é a mesma** — não há geração de objeto, e o
equivalente mais próximo depende de `If-Match` com ETag, que nem toda operação
aceita. Supor paridade seria o mesmo erro de supor que `INSERT ROW` casa por
nome.
