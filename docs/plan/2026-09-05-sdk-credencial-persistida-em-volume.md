# A credencial rotacionada precisa sobreviver ao pod

**Escrito em** 2026-09-05 · **Base** `sdk/v0.27.2` · **Alvo** `sdk/v0.28.0` +
motor `0.4.0`

> **PASSOS 1, 5 e 6 EXECUTADOS** em 2026-09-05 — os três que a §"Como entregar
> isto a um agente" atribui a um agente no `brevis`. O passo 1 saiu sozinho, na
> `sdk/v0.27.3`, como a spec pede; o 6 na `sdk/v0.28.0`; o 5 está em `master`,
> aguardando a release do motor.
>
> **Os passos 2, 3, 4 e 7 continuam abertos** e são de pessoa: console GCP,
> `zarv-applications` e o fetcher. O §9 abaixo registra o que saiu diferente da
> spec e por quê.

Pedido de quem consome:

> Precisamos ter o refresh funcionando, porque não vamos conseguir ficar
> controlando a env. Precisamos salvar esse valor localmente — no pod ele morre.
> Não podemos salvar no BigQuery nem no banco de origem; e pensando que o cliente
> use só o SDK, sem a plataforma, uma tabela no Brevis também não serve. A ideia
> do volume me parece mais justa. Isso pode ser mais um atalho para o cliente,
> não uma solução federada do SDK.

Está certo, e o raciocínio que ele já fez sozinho — descartar a tabela porque o
cliente pode não ter a plataforma — é o que define a forma da solução.

---

## Como entregar isto a um agente

Esta spec **não é executável de ponta a ponta por um agente só**, e vale saber
onde ele para:

| passos | quem executa | por quê |
|---|---|---|
| 1 e 6 | **um agente no `brevis`** | é código do SDK, e a spec basta |
| 5 | um agente no `brevis` | é o motor; ver a ressalva do §6.1 sobre o nome da imagem |
| 2, 3 e 4 | **uma pessoa** | dependem de console GCP e de um segundo repositório (`zarv-applications`, via ArgoCD) |
| 7 | quem tiver o dev de pé | depende de 2–5 estarem feitos |

E o **passo 1 é uma tarefa própria**, não parte desta: o §9 do
[`SDK_V9.md`](../SDK_V9.md) tem repro, conserto e prova escritos. Entregue-o
primeiro e sozinho — um agente que receber só esta spec vai fazer o 6 sobre um
`Refresh` que ainda não funciona, e o 7 vai devolver
`refresh response has no field "expires"`.

## 0. Pré-requisito: sem o §9 isto não resolve nada

O [§9 do `SDK_V9.md`](../SDK_V9.md) é bloqueante para esta feature. Hoje a
requisição de renovação vai **sem a credencial** — o jar é semeado pela URL da
fonte e o `cookiejar` usa o diretório dela como `Path` padrão, então
`/api/auth/session` não recebe o cookie de `/api/proxy/…`.

Persistir o resultado de uma renovação que não renova nada é guardar lixo. **O §9
vem primeiro**, e sem ele o resto desta spec não tem o que salvar.

---

## 1. Por que a env não escala, e por que uma chave estática sim

Hoje a credencial inteira vive numa env var. Ela **rotaciona**, então alguém
recola por janela — e é isso que o consumidor quer parar de fazer.

A troca que resolve:

| hoje | proposto |
|---|---|
| a env guarda o **valor rotativo** → recolar toda janela | a env guarda uma **chave estática** → colar uma vez, nunca mais |
| o valor novo morre com o pod | o valor novo é gravado no volume, cifrado com a chave |

**É essa assimetria que faz a feature valer.** Não é "trocar env por arquivo": é
trocar um segredo que muda por um que não muda.

---

## 2. A forma: o SDK escreve um arquivo, a plataforma fornece o caminho

O SDK **não** aprende Kubernetes, nem GCS, nem banco. Ele lê e escreve um
arquivo num diretório que alguém lhe disse. Quem monta o volume é problema da
plataforma — e é isso que mantém a feature como atalho e não como solução
federada.

```go
Auth: &from.Credential{
    Value: from.FromEnv("GABRIEL_SESSION_COOKIE"),   // a semente, uma vez
    Apply: from.AsCookie,
    Refresh: &from.Refresh{
        URL:       "https://365.gabriel.com.br/api/auth/session",
        ExpiresAt: from.JSONField("expires"),
        WarnAfter: 7 * 24 * time.Hour,

        // NOVO: onde guardar o valor rotacionado. Sem isto, nada muda em
        // relação a hoje — o valor vale só para esta execução.
        Store: from.FileStore{Name: "gabriel-session"},
    },
},
```

`FileStore` resolve o diretório assim, e nesta ordem:

1. o campo `Dir`, se preenchido;
2. `BREVIS_CREDENTIAL_DIR`, que é o que a plataforma injeta;
3. **nada** — e aí o store fica desligado, com um log dizendo que ficou.

Rodando local, `BREVIS_CREDENTIAL_DIR=./.brevis` e acabou. Rodando sob o Brevis,
o motor monta o volume e injeta a variável. **O mesmo código nos dois.**

### A ordem de leitura, que é o ponto todo

1. há arquivo no store e ele decifra → usa o valor de lá;
2. não há, ou não decifra → usa `Value` (a semente da env);
3. renova, e grava o resultado no store.

A semente deixa de ser o que se gerencia e passa a ser o que se usa **uma vez**.

---

## 3. Cifrado, e por quê

O arquivo é AES-256-GCM, chave de `BREVIS_CREDENTIAL_KEY` (32 bytes em base64).
**Sem chave, o store recusa a ligar** — e recusa dizendo isso, não gravando em
claro.

Três razões, e a terceira é a que fecha a discussão:

- o volume é **compartilhado**: um `ReadWriteMany` é montado por todos os pods de
  passo, então qualquer fetcher lê o arquivo de qualquer outro;
- um volume vira snapshot, e snapshot vira backup, e backup vira um lugar onde
  ninguém lembra que há credencial;
- **acabamos de tirar exatamente isto de uma tabela do BigQuery.** Repetir o erro
  num arquivo, por ser um arquivo, seria não ter aprendido nada.

A chave é estática: entra uma vez no secret, e nunca mais. É o oposto do cookie.

---

### O formato em disco, que precisa ser decidido agora

Um arquivo persistido é um contrato: mudar depois exige migração, e migração de
credencial é a que ninguém quer fazer às pressas. Então fica definido:

```
brevis-cred/1\n            <- versão, em texto, primeira linha
<nonce 12 bytes><ciphertext+tag>   <- AES-256-GCM, binário
```

- **A versão na primeira linha** é o que permite mudar o resto sem adivinhação.
  Um leitor que não reconhece a versão trata como ausente e cai na semente — não
  falha, porque uma versão futura num volume compartilhado é cenário normal
  durante um rollout.
- **O nonce vai no arquivo**, à frente do texto cifrado, e é sorteado a cada
  escrita. Reusar nonce com a mesma chave em GCM quebra a cifra, e é o erro mais
  comum de quem implementa isso pela primeira vez.
- **Nada de metadado no arquivo** — nem `expires`, nem `user`, nem quando foi
  gravado. O `mtime` já diz o quando, e o resto é derivável ou envelhece.

## 4. O que o SDK precisa acertar, e que é fácil errar

**Escrita atômica.** Grava num temporário no mesmo diretório e `rename`. Um pod
morto no meio da escrita não pode deixar um arquivo pela metade — que
decifraria com erro e mandaria o próximo run para a semente, silenciosamente.

**Concorrência.** Dois pods renovando ao mesmo tempo gravam dois valores. Com
`concurrency: 1` no workflow isso não acontece, mas o SDK não pode supor o
orquestrador. Um lock por `O_EXCL` com expiração, ou último-a-escrever-vence
**documentado**. A escolha depende de um fato do fornecedor: **verificado no
Gabriel que rotacionar não invalida o token anterior**, então último-vence é
seguro ali; para um fornecedor que invalide, não é.

**Permissões.** Arquivo `0600`, diretório `0700`. E se o diretório vier com
permissão mais frouxa, recusar — um volume compartilhado com `0777` é um
diretório público.

**Falha ao gravar não derruba a execução, mas grita.** A carga já aconteceu; o
que se perdeu foi a rotação. Vai como `ERROR` no log **e** em `Result`, como o
`CredentialExpiry` já vai — porque um aviso que só existe no log é a morte
silenciosa com passos a mais.

**O nome do arquivo é dado pelo chamador** (`Name: "gabriel-session"`), nunca
derivado da URL — URL carrega segredo em query string, e nome de arquivo vaza
para log, listagem e backup.

---

## 5. O lado da plataforma: o motor precisa montar o volume

Hoje ele não monta nada. `internal/execution/kubernetes/pod.go` tem `PodSpec` sem
`Volumes` e `Container` sem `VolumeMounts`.

O que entra, seguindo o padrão que o motor já usa para secrets
(`BREVIS_POD_ENV_FROM_SECRETS`):

```
BREVIS_POD_CREDENTIAL_PVC   nome do PersistentVolumeClaim
BREVIS_POD_CREDENTIAL_PATH  onde montar; default /var/brevis/credentials
```

Com os dois definidos, todo pod de passo ganha o volume e a env
`BREVIS_CREDENTIAL_DIR` apontando para o mount. Sem eles, nada muda — e é assim
que a feature continua sendo atalho, não requisito.

**E uma anotação, que é fácil não descobrir.** O GCS Fuse no GKE injeta um
sidecar, e ele só entra quando o pod traz:

```yaml
metadata:
  annotations:
    gke-gcsfuse/volumes: "true"
```

Sem ela o pod sobe, o volume **não** monta, e o erro aparece como
"no such file or directory" no caminho da credencial — apontando para o SDK, que
não tem culpa. O motor já escreve anotações próprias em
`internal/execution/kubernetes/pod.go:372` (`brevis.dev/workflow`, `/node`,
`/run`), então o lugar existe; o que falta é ela ser configurável, porque é
específica do GKE e não pode ficar embutida num motor que também roda em outro
lugar.

Sugestão: `BREVIS_POD_ANNOTATIONS` no formato `chave=valor,chave=valor`, do mesmo
jeito que `BREVIS_POD_ENV_FROM_SECRETS` já é uma lista. Assim a anotação do
gcsfuse é config de instalação, não código.

### O cluster é GKE, não EKS

O pedido fala em EKS. O cluster de dev é **GKE**:
`gke_zarv-development-94b6_us-central1_gke-dev-cluster`. Isso muda a resposta,
porque `ReadWriteMany` no GKE não é EFS.

| opção | como | custo | veredito |
|---|---|---|---|
| **GCS Fuse CSI** | driver nativo do GKE monta um bucket como diretório | centavos — são alguns KB | **recomendado**: RWX de verdade, e o bucket já é infra que existe |
| **Filestore** | `filestore.csi.storage.gke.io`, RWX real | a instância mínima é **1 TiB** | caro demais para guardar cookies |
| **Persistent Disk RWO** | disco por nó | barato | **não serve**: dois pods em nós diferentes não compartilham |

Recomendação: **GCS Fuse**. O SDK continua fazendo `open`/`write`/`rename` e não
sabe que há um bucket embaixo — que é exatamente o desenho. Confirmar antes que o
driver está habilitado no cluster (`gcsFuseCsiDriver` no addon config) e que a
service account tem `roles/storage.objectAdmin` no bucket.

Uma ressalva honesta sobre o gcsfuse: `rename` nele **não é atômico** como num
POSIX de verdade. Para um arquivo de poucos KB escrito por um pod de cada vez o
risco é baixo, mas precisa estar escrito — e é mais um argumento para o lock.

---

## 6. O que não fazer

- **Não** colocar isto no BigQuery, nem no banco de origem do cliente, nem numa
  tabela do Brevis. As três já foram descartadas com razão: as duas primeiras por
  serem do cliente e analíticas, a terceira porque o cliente pode usar só o SDK.
- **Não** fazer o SDK falar com Kubernetes, GCS ou Secret Manager. Ele abre um
  arquivo. Tudo que for específico de nuvem mora na plataforma, e é o que permite
  a mesma feature rodar em `./.brevis` na máquina de alguém.
- **Não** gravar em claro quando a chave faltar. Recusar, dizendo o que falta.
- **Não** tornar obrigatório. Sem `Store` e sem `BREVIS_CREDENTIAL_DIR`, o
  comportamento é exatamente o de hoje.
- **Não** guardar mais do que a credencial. Nem a resposta da renovação, nem
  `user`, nem o `expires` — este último é derivável e envelhece.

---

## 6.1 A ordem dos passos, e quem faz cada um

Nada aqui pode começar pelo meio. A ordem importa porque três dos passos são de
repositórios e times diferentes.

| # | passo | onde | bloqueia |
|---|---|---|---|
| 1 | **Consertar o §9** | `brevis/sdk` | tudo — sem ele não há o que salvar |
| 2 | Confirmar o **GCS Fuse CSI** habilitado no cluster de dev | GKE, addon `gcsFuseCsiDriver` | o 4 |
| 3 | Criar o **bucket** e dar `roles/storage.objectAdmin` à service account dos pods | GCP | o 4 |
| 4 | **PVC + deploy** apontando para o bucket | `zarv-applications`, via ArgoCD | o 6 |
| 5 | `Volumes`/`VolumeMounts` no `PodSpec` e as duas env vars | `brevis` motor → release `0.4.0` | o 6 |
| 6 | `Refresh.Store` e o `FileStore` | `brevis/sdk` → `v0.28.0` | o 7 |
| 7 | Religar o `Refresh` no fetcher e provar | `zarv-data-pipeline` | — |

Os passos **2 e 3 podem ser feitos hoje**, em paralelo com o 1, e são os únicos
que dependem de acesso a console.

### Duas coisas que travam a cadeia e não são desta spec

**A imagem do motor ainda se chama `bravis`.** `daniel3843/brevis` existe no
Docker Hub **sem tags**, e a `VERSION 0.3.0` foi marcada antes da renomeação —
então a imagem publicada ainda lê `BRAVIS_*`. O passo 5 produz uma release nova;
é a hora de resolver isso junto, ou a env do volume nasce com o prefixo errado.

**O passo 7 depende do 1 para valer.** Religar o `Refresh` sem o §9 devolve o
erro `refresh response has no field "expires"` — que foi como esta sequência
começou.

## 7. Critério de pronto

**SDK (`v0.28.0`)**

1. §9 consertado. Sem ele, nada aqui tem efeito.
2. `Refresh.Store` opcional; ausente, o comportamento é o de hoje.
3. `FileStore` resolve o diretório por `Dir` → `BREVIS_CREDENTIAL_DIR` → nada, e
   loga qual venceu.
4. AES-256-GCM com chave de `BREVIS_CREDENTIAL_KEY`; sem chave, recusa **na
   montagem**, não em runtime.
5. Escrita atômica (temp + rename), arquivo `0600`, diretório `0700` e recusa a
   diretório frouxo.
6. Ordem de leitura: store → semente → renova → grava. Teste que cobre as três.
7. Falha ao gravar não derruba a execução, e aparece em `Result`.
8. A credencial nunca em log, nem truncada, nem no nome do arquivo.
9. Teste com dois processos concorrentes, provando o comportamento escolhido —
   seja lock, seja último-vence documentado.

**Motor (`0.4.0`)**

10. `PodSpec.Volumes` e `Container.VolumeMounts` existem.
11. `BREVIS_POD_CREDENTIAL_PVC` e `..._PATH` montam o volume em todo pod de passo
    e injetam `BREVIS_CREDENTIAL_DIR`. Ausentes, nada muda.
12. Documentado em `docs/KUBERNETES.md`, com o PVC de exemplo para GCS Fuse.

**A prova**

13. O `gabriel` roda duas vezes em dev com `GABRIEL_SESSION_COOKIE` **removida
    depois da primeira**. A segunda execução autentica com o que veio do volume.
    É a única prova que importa: é literalmente o que o consumidor pediu.

---

## 8. Sobre o cache que vem depois

O pedido menciona uma camada de cache futura. Ela encaixa aqui sem redesenho: se
`FileStore` for uma implementação de uma interface pequena — `Load`/`Save` — um
`RedisStore` entra ao lado sem que o `Refresh` saiba a diferença.

Mas **não construa a interface agora por causa disso**. Uma abstração com uma
implementação só é um palpite sobre a segunda; quando o Redis existir, o formato
dele vai ensinar coisas que hoje seriam adivinhadas. O que se deve fazer agora é
**não impedir**: manter a leitura e a escrita atrás de duas funções, e não
espalhar `os.ReadFile` pelo caminho da renovação.

---

## 9. O que saiu diferente, e o que ficou para o passo 4

### O §9 do `SDK_V9.md` não se resolvia com uma linha

A spec dá duas opções: semear o jar com `Path=/`, ou aplicar a credencial no
header da renovação. **Nenhuma das duas sozinha bastava**, e isso só apareceu ao
escrever a asserção certa.

`Path=/` conserta a ida: a renovação passa a receber a credencial. Mas o cookie
que ela **reemite** volta a ficar preso, agora em `/api/auth`, e as páginas
seguem com o valor velho. A renovação renova para ninguém — o mesmo defeito na
direção oposta, e o `Store` gravaria um valor que nunca foi usado.

A credencial deixou de ser cookie de jar e passou a ser **cabeçalho**, que vale
para toda requisição independentemente de path. Um `credentialJar` desvia os
nomes da credencial antes que o jar os guarde, o que mantém a invariante da
`v0.26.0` (cada cookie num lugar só) e dá de brinde o que esta spec precisa: **o
valor rotacionado fica na mão**, em vez de enterrado no jar.

A rotação também é aplicada no laço de páginas, e não só após a renovação: uma
API pode reemitir a sessão em qualquer resposta. Sem isso, uma regressão que o
teste da `v0.26.0` pegou.

### `FileStore` é o store, e não uma descrição dele

A spec escreve `Store: from.FileStore{Name: "..."}` — por valor. Então
`FileStore` implementa a interface diretamente, resolvendo diretório e chave a
cada chamada, em vez de haver um `OpenFileStore` que devolve outra coisa. A
recusa na montagem (critério 4) veio por uma interface opcional,
`CredentialStoreChecker`, que o `Credential.Check` consulta.

### O `Chmod` explícito saiu

A primeira versão fazia `tmp.Chmod(0600)` depois de `os.CreateTemp`. É
redundante — `CreateTemp` já cria a `0600` — e num gcsfuse um `chmod` é no-op ou
erro, dependendo da montagem. Uma linha que não faz nada em disco normal e
quebra no destino real.

### O que isso exige do passo 4, e que a spec não previa

O critério 5 manda **recusar diretório com permissão frouxa**, e um mount de
gcsfuse vem `0755` por padrão. Então o PV **precisa** de `mountOptions`, ou o
store recusa a ligar no lugar onde ele foi feito para rodar:

```yaml
mountOptions: [implicit-dirs, uid=0, gid=0, dir-mode=0700, file-mode=0600]
```

Está no `docs/KUBERNETES.md` com o PV inteiro. Sem essas duas linhas, o passo 7
falha na montagem com uma mensagem que diz exatamente isso.

### A prova do critério 13, no que dá para provar sem o cluster

`TestSegundaExecucaoUsaOQueVeioDoVolume` roda a extração duas vezes contra um
servidor local, com a semente **removida depois da primeira**, e afirma que a
segunda autenticou com o valor que a primeira gravou. É a forma do critério 13
sem o GKE; o critério 13 de verdade continua sendo o passo 7.
