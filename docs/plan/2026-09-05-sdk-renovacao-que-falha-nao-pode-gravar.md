# Uma renovação que falha não pode gravar no store

**Escrito em** 2026-09-05 · **Base** `sdk/v0.29.0` · **Alvo** `sdk/v0.29.1`

Tarefa única e pequena. É o §10 do [`SDK_V9.md`](../SDK_V9.md), extraído para ser
executado sozinho.

Achado rodando a prova do critério 11 da spec da credencial persistida, contra a
API real com uma credencial vencida — que é a única forma de ele aparecer.

---

## O defeito

`sdk/extract/auth.go`, dentro de `renew` (linhas 191–202):

```go
if aplicarRotacao(source, jar.Rotacoes()) {
    guardar(ctx, r.Store, http.Header(source.Header).Get("Cookie"), stats)   // 192: GRAVA
}

if r.ExpiresAt == nil {
    return nil
}
expires, err := r.ExpiresAt(body)
if err != nil {
    return fmt.Errorf("refresh %s: %w", redactURL(r.URL), err)               // 201: falha
}
```

**A gravação acontece antes da checagem de validade.** Uma renovação que responde
sem `expires` — ou seja, uma que não autenticou — grava assim mesmo o que o
servidor devolveu.

## Por que é grave, e não só feio

O NextAuth, para uma sessão não autenticada, responde **200** com corpo `null` e
`Set-Cookie` **limpando os valores**. Medido contra a API real:

```
semente colada por um humano   1174 caracteres
o que foi parar no store        419 caracteres   <- mesmos nomes, valores esvaziados
```

É a credencial de uma sessão **deslogada**, gravada por cima.

E a ordem de leitura é **store antes da semente** (`internal/core/auth.go:127`),
o que está certo: o guardado é o resultado da última rotação. A combinação é que
mata:

1. a renovação falha e grava o cookie deslogado;
2. da próxima vez o store vence a semente;
3. **trocar a env por uma credencial boa deixa de resolver.** O valor ruim ganha
   sempre, e a única saída é apagar o objeto à mão.

Um erro de rede não causa isso — sem resposta não há rotação. O caso que envenena
é o mais provável de todos: **a credencial venceu**, a renovação volta não
autenticada, e o store guarda a prova disso por cima do que ainda podia servir.

E o sintoma para quem opera é `401` sem explicação, num pipeline que ontem
funcionava. Ninguém vai suspeitar do store.

## Não é uma armadilha nova

O vendor em Python que originou tudo isto já a conhecia. O `seed_cookie`
verificava **antes** de gravar, e o comentário dizia por quê:

> *verificar antes da escrita evita que um valor morto pouse como a linha mais
> nova, onde ele ofuscaria o que já está guardado*

Mesma armadilha, store diferente.

---

## O conserto

Mover o `guardar` para **depois** de a renovação ser dada por boa, e não gravar em
nenhum caminho de erro:

```go
rotacionou := aplicarRotacao(source, jar.Rotacoes())

if r.ExpiresAt == nil {
    // Sem sinal de validade não há o que conferir, e o chamador abriu mão dele.
    if rotacionou {
        guardar(ctx, r.Store, http.Header(source.Header).Get("Cookie"), stats)
    }
    return nil
}

expires, err := r.ExpiresAt(body)
if err != nil {
    return fmt.Errorf("refresh %s: %w", redactURL(r.URL), err)
}

if rotacionou {
    guardar(ctx, r.Store, http.Header(source.Header).Get("Cookie"), stats)
}
```

O `aplicarRotacao` continua rodando cedo — a credencial rotacionada tem de valer
para as páginas desta execução mesmo que o `ExpiresAt` falhe depois. **O que muda
é só quando ela é persistida.**

### O limite que sobra, e precisa ser dito

Com `ExpiresAt == nil` o SDK **não tem sinal nenhum** de que a renovação
autenticou: o status é 200 nos dois casos, e o corpo é o único lugar onde a
diferença aparece. Então o store continua envenenável nessa configuração.

Por isso: **`Store` sem `ExpiresAt` deve avisar na montagem**, dizendo que sem
sinal de validade uma renovação não autenticada será persistida. Não recusar —
há fontes cuja renovação não devolve validade nenhuma, e para elas o store ainda
vale. Mas quem escolhe isso tem de escolher sabendo.

---

## Critério de pronto

1. Uma renovação cujo corpo não satisfaz o `ExpiresAt` **não grava**. Teste com
   um servidor que devolve 200, um `Set-Cookie` qualquer e corpo sem `expires`:
   depois dele o store continua **vazio**.
2. Uma renovação boa continua gravando. O teste que já existe cobre isso e não
   pode ser alterado para passar.
3. Com `ExpiresAt == nil`, grava — e há teste dizendo que grava.
4. `Store` sem `ExpiresAt` avisa na montagem, com o motivo. Teste da mensagem.
5. A credencial rotacionada continua valendo para as páginas desta execução mesmo
   quando o `ExpiresAt` falha depois — ou seja, `aplicarRotacao` não se move.
6. `go test ./... -short` verde e `go vet ./...` limpo.

## A prova, fora do teste

Com o `gabriel` do consumidor apontado para uma credencial **vencida**: rodar, e
conferir que `gs://zarv-data-pipeline-credentials/gabriel-session` **não** foi
criado nem alterado. Hoje ele é criado com 419 bytes de sessão deslogada.
