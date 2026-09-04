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

O eixo que ninguém trata igual duas vezes: **a credencial pode viver mais que a
execução.** O `gabriel` guarda no BigQuery porque o cookie rotaciona e o próximo
run precisa do valor novo; o `ana` guarda em memória porque o token vale 1h e a
execução inteira cabe nisso.

Uma forma possível:

```go
From: from.HTTP{
    URL: "...",
    Auth: from.Rolling{
        // Onde a credencial vive entre execuções. Nil = só em memória.
        Store: store.BigQuery{Table: "bronze.vendors_gabriel_session"},

        // Como renovar. Roda ANTES do fetch, e o valor novo é persistido na
        // hora — não depois da carga.
        Refresh: func(ctx context.Context, atual string) (string, error) { ... },

        // Semente para o primeiro uso, quando não há nada guardado.
        Seed: os.Getenv("GABRIEL_SESSION_COOKIE"),

        // Renova só quando falta menos que isto para expirar.
        TTL: 30 * 24 * time.Hour,
    },
}
```

Três decisões que o SDK deve **tomar**, porque cada consumidor as toma diferente
e uma delas é sutil:

1. **Renovar antes de buscar, e persistir na hora.** O comentário do vendor em
   Python explica: a chamada de renovação é o que move a janela, e ela não pode
   ser perdida porque a busca falhou depois. Persistir num `finally` depois da
   carga — que foi o que o Python fez — é mais frágil e mais código.
2. **Serializar a renovação.** O `ana` precisou de um lock porque a API bloqueia
   IP em rajada de autenticação. Isso não é detalhe do vendor, é propriedade de
   qualquer API com login.
3. **Histórico, não sobrescrita.** O `gabriel` faz `INSERT` e não `UPDATE`, para
   responder "desde quando a sessão está parada?" quando a ingestão silencia.

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

1. `from.HTTP.Auth` cobre os dois eixos: credencial em memória (TTL) e
   credencial persistida entre execuções.
2. A renovação roda **antes** do fetch e persiste imediatamente. Teste que prova
   que uma falha no fetch **não** desfaz a rotação.
3. A renovação é serializada. Teste com concorrência.
4. O store é um valor com interface pequena — BigQuery primeiro, porque é o que
   existe; arquivo e memória saem de graça e servem aos testes.
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
