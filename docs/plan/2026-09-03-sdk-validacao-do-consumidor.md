# A validação é do consumidor — e ela é por **resposta**, não por registro

**Escrito em** 2026-09-03 · **Base** `sdk/v0.16.0` · **Alvo** `sdk/v0.17.0`
(quebra compatibilidade)

Pedido de quem consome o SDK:

> Devemos deixar a lógica de validar o payload dentro do Transform. Isto aqui
> não devemos mais usar: `Guard: sdk.RejectIf("error")`,
> `Expand: sdk.ParallelArrays(...)`. No Transform deve receber o payload quando
> for 200, 204 ou status de sucesso, e devemos conseguir retornar
> `sdk.Reject("aqui houve um erro")`. A autonomia de validar deve ficar dentro
> do Transform.

**A direção está certa e o SDK já caminhou para lá** — na `v0.15.0` a composição
das colunas saiu do SDK e foi para o `Transform`. Validar é a mesma classe de
decisão: conhecimento sobre a fonte, que só o fetcher tem.

Mas o pedido, ao pé da letra, colocaria a validação um nível abaixo de onde ela
acontece, e três coisas se perderiam. Esta spec implementa a autonomia pedida no
nível certo, e recolhe dois defeitos reais que apareceram ao verificar o pedido.

---

## 1. Metade do pedido já existe, e ninguém consegue ver

Os dois campos que o pedido quer abandonar **já são funções livres**:

```go
// internal/core/types.go:263
Guard func(status int, body []byte) error

// internal/core/types.go:299
Expand func(payload any) ([]any, error)
```

`RejectIf` e `ParallelArrays` são atalhos, não a interface. Qualquer fetcher pode
escrever a função dele hoje, com Go, e recusar como quiser.

E a recusa por registro também existe:

```go
// transform.go:36
var SkipRecord = errors.New("skip record")
```

Um `Transformer` que devolve `SkipRecord` derruba **aquele registro** sem falhar
a execução (`transform.go:106`); um que devolve qualquer outro erro falha a
execução nomeando o transformer.

**Então o problema não é falta de autonomia. É que a autonomia é invisível.**
Quem lê `Guard: sdk.RejectIf("error")` conclui que o SDK só sabe testar um campo
de topo. Quem lê `Expand: sdk.ParallelArrays(...)` conclui que só sabe arrays
paralelos. Os dois atalhos ocuparam o lugar da interface na cabeça de quem usa —
que é o mesmo modo de falhar do `ExtraMetadata`, resolvido na `v0.15.0`: um
atalho que esconde a decisão acaba tomando a decisão.

---

## 2. Por que a validação não pode descer para o `Transform`

`Transformer` roda **por registro**, depois do `Expand` (`transform.go:77`, dentro
do laço sobre os envelopes). `Guard` e `Expand` rodam **por resposta**. Mover a
validação para o `Transform` inverte a ordem, e o caso do consumidor mostra o
resultado:

O Open-Meteo recusa com **200** e este corpo:

```json
{"error": true, "reason": "Cannot initialize WeatherVariable from invalid String value tempeture_2m"}
```

| hoje | com a validação no Transform |
|---|---|
| `Guard` vê o corpo cru e falha dizendo `flagged with "error": Cannot initialize WeatherVariable...` | `Expand` roda primeiro, não acha `hourly`, e falha com "hourly não encontrado" |

A razão que o vendor deu **se perde**, e o erro passa a apontar para o lugar
errado. Pior: uma resposta de erro produz **zero registros**, então um validador
por registro nunca é chamado sobre ela — a falha chegaria como "0 linhas", que é
o silêncio que este SDK passou a semana inteira combatendo.

Some-se o custo: `Guard` recebe bytes **antes** de decodificar. Validar depois é
decodificar e expandir um corpo que já se sabia ser lixo.

**A correção do pedido, então, é de nível e não de intenção:** a autonomia vai
para uma função do consumidor, mas ela roda por resposta.

---

## 3. Dois defeitos achados ao conferir o pedido

### 3.1 Só **200** passa — `204` e os demais 2xx falham a execução

`extract.go:373`:

```go
if resp.StatusCode == http.StatusOK {
    // segue
}
...
return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
```

Um `204 No Content` — que é como um endpoint incremental diz "nada na sua
janela" — derruba a execução com `http 204`. Também `201`, `206` e `202`.

Quem pediu esta spec chamou isso de fora ("200, 204 ou status de sucesso"), e
está certo: **um vendor que responde 204 numa janela vazia não pode ser um
pipeline vermelho**. Zero registros é um resultado, não uma falha.

### 3.2 `RejectIf` aceita em silêncio o corpo que não é JSON

`expand.go:148-151`:

```go
var doc map[string]any
if err := json.Unmarshal(body, &doc); err != nil {
    return nil                      // aceita
}
```

Uma página HTML de erro devolvida com 200 — portal de gov fazendo manutenção,
WAF, proxy — passa pela guarda e vai para o decoder, que falha depois com uma
mensagem sobre JSON inválido em vez de "a resposta não é JSON". A guarda existe
justamente para esse caso e é o único que ela deixa passar.

---

## 4. A forma proposta

Uma função do consumidor, por resposta, que **valida e fatia no mesmo lugar**:

```go
Source: sdk.Source{
    URL: "https://api.open-meteo.com/v1/forecast?...",

    // Records recebe a resposta de sucesso e devolve os registros que ela
    // carrega -- ou recusa, dizendo por quê. Substitui Guard e Expand: as duas
    // eram a mesma pergunta ("o que esta resposta significa?") partida em duas.
    Records: func(r sdk.Response) ([]any, error) {
        if r.Status == 204 {
            return nil, nil          // janela vazia nao e erro
        }

        doc, err := r.JSON()         // decodifica quando o fetcher quiser
        if err != nil {
            return nil, err
        }
        if erro, _ := doc["error"].(bool); erro {
            return nil, sdk.Reject("open-meteo recusou: %v", doc["reason"])
        }

        return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
    },
},
```

O que a `Response` precisa dar, e nada além: `Status int`, `Bytes() []byte`
(cru, sem decodificar — preserva a recusa barata do `Guard` de hoje),
`JSON() (any, error)` e os headers.

### `sdk.Reject`

`sdk.Reject(formato, args...)` é o modo nomeado de recusar, usável do `Records`
**e** de um `Transformer`. Hoje um `fmt.Errorf` já falha a execução, mas não se
distingue de um erro de programação; `Reject` diz "a fonte mandou algo que não é
dado", e é isso que o log e o alerta precisam separar.

Ao lado dele, `sdk.SkipRecord` já existe e resolve o caso por registro. Ele está
documentado em `transform.go:24` e **não aparece em nenhum exemplo** — quem pediu
esta spec não sabia que existia, o que é o mesmo problema do §1.

### Os atalhos continuam, no lugar certo

`ParallelArrays`, `RejectIf` e companhia **não somem**: viram funções que se
chamam de dentro do `Records`, como no exemplo acima. O que muda é que deixam de
ocupar o campo, e portanto deixam de parecer a única forma.

---

## 5. O que isso produz no consumidor

Antes (`zarv-data-pipeline/scripts/vendors/exemplo_go/main.go`):

```go
Guard:  sdk.RejectIf("error"),
Expand: sdk.ParallelArrays("hourly", "time", "temperature_2m"),
```

Duas linhas que não dizem o que a fonte faz de errado, e que ninguém consegue
estender sem descobrir, lendo o SDK, que os campos aceitam função.

Depois: o bloco `Records` do §4 — mais linhas, e elas dizem que o Open-Meteo
recusa com 200 e um `error` no corpo, e que a razão vem em `reason`. É a mesma
troca que a `v0.15.0` fez com as colunas, e pelo mesmo motivo.

---

## 6. O que não fazer

- **Não** mova a validação para o `Transform`. §2.
- **Não** trate `204` como registro vazio inventando um envelope. Zero registros
  é zero registros; o `extract complete` já reporta `rows=0`.
- **Não** aceite qualquer 2xx sem o fetcher decidir. `206 Partial Content` numa
  API paginada pode ser exatamente o que se quer, ou exatamente o que não se
  quer — quem sabe é o `Records`. O SDK entrega os 2xx e a decisão.
- **Não** remova `SkipRecord`. Ele é a metade por registro da mesma autonomia, e
  o que falta a ele é aparecer num exemplo.
- **Não** deixe `Records` opcional com `Guard`/`Expand` sobrevivendo em paralelo.
  Três caminhos para a mesma pergunta é o que esta spec existe para fechar.

---

## 7. Critério de pronto para a `sdk/v0.17.0`

1. `Source.Records func(Response) ([]any, error)` substitui `Guard` e `Expand`.
2. `Response` expõe `Status`, `Bytes()`, `JSON()` e headers. `Bytes()` não
   decodifica: a recusa barata continua barata.
3. Todo 2xx chega ao `Records`. Teste para `200`, `204` e `206`; um `204` que
   devolve zero registros termina a execução com sucesso e `rows=0`.
4. Não-2xx continua como está: erro com status e corpo, e retry onde já havia.
5. `sdk.Reject(formato, args...)`, distinguível de erro de programação no log.
   Teste que o distingue.
6. `RejectIf` conserta o §3.2: corpo que não é JSON é recusa nomeando isso, não
   aceitação.
7. `ParallelArrays`, `RejectIf` e os demais atalhos seguem exportados e
   documentados **como funções que se chamam de dentro do `Records`**.
8. `SkipRecord` aparece em pelo menos um exemplo executável.
9. `CHANGELOG` com o diff de migração escrito por extenso — `Guard` + `Expand`
   viram `Records` —, porque é o que o consumidor vai copiar.
10. `go test ./... -short` verde e `go vet ./...` limpo.

---

## 8. A prova, fora do repositório

Peça a alguém que nunca viu o SDK para fazer um fetcher recusar uma resposta que
chega com **200** e um corpo de erro, e explicar por que ela foi recusada.

Hoje a pessoa escreve `Guard: sdk.RejectIf("error")`, não sabe que pode escrever
outra coisa, e não consegue dizer a razão que a API deu. Quando ela escrever a
própria função e a mensagem sair no log, esta spec está cumprida.
