# O que o `gabriel` mostrou que o `from.HTTP` deveria fazer

**Escrito em** 2026-09-04 · **Base** `sdk/v0.25.1` · **Alvo** `sdk/v0.26.0`

Pedido de quem consome o SDK:

> Olhando para essa implementação do gabriel, o que podemos levar como
> responsabilidade para o SDK? O HTTP precisa contemplar bastante coisa, no
> quesito de abstrair boa parte da complexidade — essa é a vantagem do SDK.

Este documento é a resposta, medida.

---

## 1. O número

O `gabriel` em Go tem **284 linhas de código**. Delas:

| arquivo / função | linhas | é sobre o Gabriel? |
|---|---|---|
| `sessao.go` — o cookie guardado no BigQuery | 81 | **não** |
| `cookie.go` — juntar e analisar o header `Cookie` | 36 | **não** |
| `prepararSessao` | 31 | **não** |
| `renovar` — chamar o endpoint de sessão | 25 | **não** |
| `cookieRotacionado` | 10 | **não** |
| **subtotal de encanamento** | **183** | **64%** |
| a `Pipeline`, a cadeia, as URLs, a regra de descarte | ~100 | sim |

**Dois terços do fetcher não são sobre o vendor.** São sobre "autenticação
rotativa com estado que sobrevive à execução" — e é exatamente o tipo de coisa
que o SDK existe para absorver.

## 2. Não é um exemplo só

Quatro dos 24 vendors do consumidor têm alguma forma de autenticação com estado,
e **duas implementações independentes** da mesma ideia já existem:

| vendor | como obtém | onde guarda | detalhe que ninguém adivinha |
|---|---|---|---|
| `gabriel` | renova um cookie rolante (`/api/auth/session`) | **BigQuery**, entre execuções | o endpoint de dados nunca reemite o cookie; só o de sessão |
| `ana` | `POST /OAUth/v1` com id+senha → JWT | **memória**, com TTL e lock | "a ANA monitora a FREQUÊNCIA de auth e bloqueia o IP em rajada" |
| `fogocruzado` | `_login()` com e-mail+senha → Bearer | memória | — |
| `meteosat_frp` | Bearer | memória | — |

As duas primeiras resolveram o mesmo problema de formas diferentes, e nenhuma é
óbvia. É o sinal clássico de responsabilidade que pertence à biblioteca.

---

## 3. O que levar, em ordem de valor

### 3.1 Credencial com ciclo de vida — apaga 183 linhas

#### Para que serve a sessão, afinal

A pergunta é justa e a resposta é específica deste vendor: **a API do Gabriel não
tem login programático.** Conferido no cliente em Python — não há `POST /login`,
nem usuário e senha, nem `client_credentials`. O único lever é o subcomando
`seed_cookie`, onde **um humano cola um cookie** obtido no navegador.

Esse cookie é um `__Secure-authjs.session-token` do NextAuth, com expiração
deslizante de 30 dias. Só `/api/auth/session` o reemite — o endpoint de dados
nunca o faz, verificado contra a API pelo autor do vendor em Python.

Então a sessão faz uma coisa só, e ela é real:

> **mantém viva uma credencial que um humano emitiu**, chamando o endpoint de
> renovação a cada execução para empurrar a janela de 30 dias.

Sem ela, alguém cola um cookie novo por mês, e a pipeline morre calada no dia 31.
Com ela, a credencial se mantém indefinidamente enquanto o pipeline rodar.

#### E a credencial é de uma PESSOA

Chamando `/api/auth/session` com o cookie guardado, a resposta é:

```
user: { name, email, image, id, marketingOptIn }
expires: 2026-10-04T22:15:07.197Z
loginNonce: mthgirnwlwx65i
```

Uma conta de usuário nomeada — não uma integração máquina-a-máquina. **A pipeline
se autentica como uma pessoa do time.** Três consequências:

- a ingestão herda as permissões dela, e para com 401 no dia em que o acesso for
  revogado ou ela sair da empresa — sem dizer que a causa é RH;
- toda ação da pipeline aparece como sendo dela nos logs do fornecedor;
- quem lê o token age como ela.

Esse último ponto recoloca o problema do armazenamento. Não são "onze
credenciais" num dataset analítico: são **onze cópias da credencial pessoal de
alguém**, legíveis por qualquer `dataViewer` de `bronze`.

**A sessão faz sentido. O lugar onde ela estava guardada é que não.**

#### Por que o BigQuery está errado, e o motivo mais forte não é o óbvio

O argumento imediato é de propósito: um warehouse analítico é para dado que se
analisa, e estado de sessão não é. Verdade, e suficiente para mudar.

Mas o motivo mais forte é outro. Medido em dev, hoje:

```sql
SELECT id, LENGTH(cookie), REGEXP_CONTAINS(cookie, r'authjs\.session-token'), updated_at
FROM bronze.vendors_gabriel_session
```
```
11 linhas · 1017 caracteres cada · contém authjs.session-token = true
```

São **onze credenciais vivas** num dataset cujo acesso é concedido para análise.
Quem tem `bigquery.dataViewer` em `bronze` — analista, dashboard, notebook,
serviço de exportação — pode ler o token e assumir a sessão. O dataset foi
liberado para ver dado de vendor, não para guardar segredo, e ninguém que
concedeu esse acesso sabe que ele passou a incluir isto.

E está no dataset `bronze`, cujo significado é "dado bruto do fornecedor". Não é
só o banco errado: é a camada errada dentro do banco errado.

#### Correção do que este documento dizia antes

A primeira versão desta análise juntou quatro vendors sob "autenticação com
estado". Olhando de novo, **só um precisa de estado que sobreviva à execução**:

| vendor | tem login programático? | precisa persistir entre runs? |
|---|---|---|
| `gabriel` | **não** — cookie colado por humano | **sim**, ou a credencial morre em 30 dias |
| `ana` | sim, `POST /OAUth/v1` com id+senha | não — o token vale 1h e a execução cabe nisso |
| `fogocruzado` | sim, `_login()` com e-mail+senha | não |
| `meteosat_frp` | Bearer | não |

Isso muda o desenho. O caso **comum** é credencial em memória com TTL e trava —
o que o `ana` já faz, e cujo motivo está escrito lá: *"a ANA monitora a
FREQUÊNCIA de auth e bloqueia o IP em rajada"*. O caso **raro** é a credencial
rotativa sem login, e é só ele que precisa de armazenamento.

Vale o armazenamento por um vendor? Sim — mas com peso de caso raro: interface
pequena, um store de verdade, e nada obrigatório para os outros três.

#### Onde a credencial deve viver

**No Secret Manager, e o SDK já tem o padrão para isso.** `store/gcs`, `store/s3`
e `to/bigquery` estabeleceram a regra: *um driver com SDK de fornecedor atrás
mora no próprio pacote*. Um `sdk/secret/gcp` segue a mesma, e quem não usa não
paga.

O encaixe é bom por três razões, não só por não ser o warehouse:

- **IAM separado.** `secretmanager.secretAccessor` é concedido a quem roda o
  pipeline, não a quem analisa dado. É a separação que hoje não existe.
- **Versão é rotação.** Cada renovação é uma versão nova do segredo — nativo, e
  entrega de graça o histórico que o `INSERT` no BigQuery existia para dar. "Desde
  quando a sessão parou de rotacionar?" vira "qual a data da última versão".
- **Auditoria.** Acesso a segredo é logado; `SELECT` num dataset analítico, não.
  Com uma credencial pessoal em jogo, saber quem a leu deixa de ser higiene e
  passa a ser o mínimo.

E uma pergunta que o SDK não resolve, mas que este caso levanta: **vale pedir ao
fornecedor uma credencial de serviço.** Uma chave de API ligada à organização, e
não à conta de uma pessoa, elimina a rotação, o store e o dia em que alguém sai
da empresa e a ingestão morre. O trabalho abaixo é o certo para quando a resposta
for não.

Uma forma possível, com o caso comum simples e o raro explícito:

```go
// O comum: credencial obtida por login, viva só durante a execução.
Auth: from.Login{
    Obtain: func(ctx context.Context) (string, error) { ... },
    TTL:    time.Hour,   // renova quando faltar menos que isto
}

// O raro: credencial rotativa que precisa sobreviver ao processo.
Auth: from.Rolling{
    Store:   secret.GCP{Name: "gabriel-session-cookie"},
    Refresh: func(ctx context.Context, atual string) (string, error) { ... },
    Seed:    os.Getenv("GABRIEL_SESSION_COOKIE"),  // o humano, uma vez
    TTL:     30 * 24 * time.Hour,
}
```

**O SDK não deve trazer um store que escreva no warehouse do cliente.** Nem como
opção: um campo `Store: bigquery.Table{...}` seria copiado do exemplo por quem
não pensou no assunto, e é assim que uma credencial acaba num dataset de
análise. Se alguém precisar mesmo, a interface é pública e ele implementa.

Três decisões que o SDK deve **tomar**, porque cada consumidor as toma diferente
e uma delas é sutil:

1. **Renovar antes de buscar, e persistir na hora.** O comentário do vendor em
   Python explica: a chamada de renovação é o que move a janela, e ela não pode
   ser perdida porque a busca falhou depois. Persistir num `finally` depois da
   carga — que foi o que o Python fez — é mais frágil e mais código.
2. **Serializar a renovação.** O `ana` precisou de uma trava porque a API bloqueia
   IP em rajada de autenticação. Isso não é detalhe do vendor, é propriedade de
   qualquer API com login.
3. **Nunca logar a credencial.** Nem truncada. O `Describe()` do driver já
   redige a URL; o mesmo cuidado vale aqui, e é fácil esquecer num log de debug.

### 3.2 Cookie jar — apaga 36 linhas e uma armadilha

O `cookie.go` existe só para juntar o `Set-Cookie` da resposta ao header atual,
preservando o que não foi reemitido. Um `net/http/cookiejar` no cliente faz isso
sozinho.

E ele carrega uma armadilha que **todo consumidor vai errar uma vez**: o cookie
do NextAuth é um JWT, e JWT tem `=` de padding. Dividir `nome=valor` em todos os
`=` corta o token, e a sessão morre com **401** — não com erro de parsing. Está
no teste `TestAnalisarCookiesDivideNoPrimeiroIgual` do consumidor, e não deveria
precisar existir lá.

### 3.3 Paginação por número de página

O `gabriel` pagina com `page=1,2,3…` e `limit` fixo. O SDK tem offset e cursor,
mas não página — então o consumidor escreveu:

```go
OffsetKey: "page",
PageSize:  1,   // não é o tamanho da página: é quanto o `page` avança
```

Funciona, e é um truque. `PageSize` significando "incremento do offset" já é
confuso; usá-lo como 1 para simular paginação por página é o tipo de coisa que
alguém copia sem entender e quebra ao mexer no `limit`.

Um `PageKey string` + `FirstPage int` resolveria, e o estilo é comum o bastante
para merecer nome próprio.

### 3.4 O erro do staging não diz o que fazer

Com 11.378 linhas, a carga passou do `InlineLimit` (5000) e tentou GCS:

```
target bronze.vendors_gabriel_occurrences refused: close gcs writer:
googleapi: Error 404: The specified bucket does not exist., notFound
```

Ele **não diz qual bucket**, nem que o padrão mudou de nome na `v0.25.0`, nem as
duas saídas (criar o bucket, ou subir o `InlineLimit`). O resto do SDK é exemplar
nisso — `Columns` diz "add them to Columns, or drop them in Transform"; o
`Metadata` nomeia os campos. Aqui não.

---

## 4. O que **não** levar

- **A regra de descarte.** "Ocorrência sem id ou sem coordenada não serve" é
  política do vendor, e o `SkipRecord` já a serve bem.
- **A forma da linha.** Foi o assunto das `v0.15.0`–`v0.24.0`, e a conclusão —
  o `Transform` compõe, o SDK não inventa — está certa. Não reabrir.
- **A consulta que define o escopo.** Um fetcher que lê o BigQuery para saber o
  que buscar é caso do consumidor, não do SDK.

---

## 5. Critério de pronto para a `sdk/v0.26.0`

1. `from.HTTP.Auth` cobre os dois casos, e o comum é o simples: credencial
   obtida por login e viva só durante a execução (3 dos 4 vendors), e credencial
   rotativa que sobrevive ao processo (1 dos 4).
2. A renovação roda **antes** do fetch e persiste imediatamente. Teste que prova
   que uma falha no fetch **não** desfaz a rotação.
3. A renovação é serializada. Teste com concorrência.
4. O store é um valor com interface pequena, e o SDK **não** traz nenhum que
   escreva no warehouse do cliente. `sdk/secret/gcp` (Secret Manager) primeiro,
   no próprio pacote, como `store/gcs`; memória e arquivo saem de graça e servem
   aos testes.
   4.1. A credencial nunca aparece em log, nem truncada.
5. Cookie jar no cliente HTTP, e o header final acessível. O teste do
   consumidor sobre o `=` do JWT passa a viver aqui.
6. `PageKey` + `FirstPage` como paginação de primeira classe, e o `PageSize: 1`
   deixa de ser necessário.
7. O erro do staging nomeia o bucket e as duas saídas.
8. O `gabriel` do consumidor encolhe de 284 para ~100 linhas, e **as 100 que
   sobram são todas sobre o Gabriel.** É esta a medida de pronto.

---

## 6. A prova

Reescreva o `gabriel` com a `v0.26.0` e conte as linhas que sobraram falando de
sessão, cookie ou BigQuery. Se sobrar alguma, ela é a próxima a mudar de lado.
