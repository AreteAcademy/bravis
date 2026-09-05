# O que cabe a uma biblioteca de ETL, e o que é do consumidor

**Escrito em** 2026-09-05 · **Base** `sdk/v0.36.0` · **Origem** contribuição de consumidor

Contribuição do `zarv-data-pipeline`, escrita **de fora do SDK**. Não é spec
executável como as outras deste diretório: são propostas com a evidência que as
motivou, para o time do SDK decidir o que aceita.

---

## De onde isto vem

O consumidor está portando **19 fetchers de Python para Go**, um a um, mantendo a
mesma tabela de destino e os mesmos ids. Dois já rodam. O levantamento dos outros
17 já está feito, então dá para saber o que vai se repetir 17 vezes — e é isso
que separa uma queixa de uma proposta.

Cada item abaixo saiu de uma medição, não de uma impressão. Onde a medição
**derrubou** a hipótese, ela está registrada como derrubada.

---

## O teste que cada pedido tem de passar

> **Um time sem relação nenhuma com quem escreveu este documento iria querer
> isto?**

Parece óbvio e não é. A primeira versão desta lista foi escrita sem ele, e o
pedido principal reprovou.

### O pedido que foi retirado, e por quê

A cadeia de transformação dos dois fetchers que já rodam tem 34 e 31 linhas de
código, das quais **27 são idênticas**. A lista `Columns` do `Target` bate byte a
byte. Dos 24 fetchers a portar, os 24 montam a mesma coisa.

Com esse número na mão, o pedido era: *"que o SDK ofereça um helper que monte
esse envelope a partir de uma declaração"*.

**O envelope é `provider`, `entity`, `source_key`, `payload`.** Isso é a
convenção de landing de um consumidor. Um time que carrega pedidos para o
Postgres não tem `provider` nem `entity`, e receberia uma API que fala de um
domínio que não é o dele. O helper economizaria 500 linhas **num** consumidor ao
custo de a biblioteca passar a ter opinião sobre o esquema de quem a usa.

Pedido retirado. O que sobrou dele — e passa no teste — está nos itens 3 e 4:
a **ordenação** que a cadeia exige é propriedade de correção e é genérica; os
nomes das colunas não são.

Fica o registro porque o teste é mais útil que o pedido: vale aplicá-lo a
qualquer coisa que entre no SDK.

---

## 1. O namespace da identidade é de um consumidor, não da biblioteca

`sdk/internal/core/types.go:117`:

```go
var namespaceDeIngestao = uuid.MustParse("e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7")
```

Esse UUID veio do `VENDOR_NAMESPACE` do pipeline **deste consumidor**. Ele está
cravado, não exportado e não configurável, dentro de uma biblioteca que vai para
todos os times.

`ColumnIngestionID` e `ColumnIngestionLoadedAt` são constantes fixas pelo mesmo
motivo. E a documentação do `IngestionID` (`sdk/ingestion.go:32`) afirma:

> the namespace and the separator are frozen: a row written here has to match the
> row a Python fetcher writes for the same record

Isso descreve a migração de **um** consumidor como se fosse o contrato da
biblioteca. Para quem começa um ETL novo em Go, "casar com um fetcher em Python"
não quer dizer nada.

O que é genérico é o conceito: **identidade determinística e estável sobre N
campos**, com algoritmo e separador congelados. O namespace, não.

**Proposta**

1. Namespace configurável, com o valor de hoje como padrão — quem já gravou não
   pode ter os ids reescritos.
2. Os nomes das colunas como **padrão sobreponível**, não constante.
3. A documentação reescrita: o contrato é "determinístico e estável"; casar com
   um fetcher em Python é um **modo** (`IngestionIDPython`), não a definição.

Nada disso muda o comportamento de quem não configurar nada. Muda o que a
biblioteca afirma ser.

---

## 2. Ler de N fontes, tolerando a falha de algumas

**É o maior, e é o que separa "cliente HTTP com transformers" de "biblioteca de
ETL".**

Todo ETL que lê de muitas origens reescreve o mesmo laço. Esta é a forma
canônica, de um fetcher real com ~5.500 origens:

```
para cada fonte:
    tenta ler
    se falhar: registra QUAL falhou, e continua
    acumula as linhas
    quando o buffer passa de N: carrega e esvazia
devolve {carregadas: N, falharam: [ids]}
```

Contra o SDK de hoje:

| o laço faz | o SDK |
|---|---|
| itera N fontes | `Data.Records` é um `iter.Seq2` e não há como encadear N `Source` num só `Data` |
| falha em uma → registra e continua | o extract **aborta** na primeira (`sdk/extract/extract.go`, os `return core.Envelope{}, err`) |
| devolve **quais** falharam | o `Result` não tem esse campo |
| espera entre chamadas | coberto pelo `RateLimiter` |
| descarrega a cada N linhas | `Load(ctx, envelopes ...core.Envelope)` recebe uma **fatia** — o lote inteiro fica em memória, e o `encodeRows` monta uma segunda cópia num `bytes.Buffer` |

**A assimetria aponta o caminho.** O `load` já tolera uma linha ruim e a reporta
em `ErrorRows`, e a documentação dele diz que `Load` sempre devolve um
`LoadResult`, falhas incluídas. O `extract` não tolera nada. A política que
existe de um lado falta do outro.

Num fan-out de 4.803 origens, a leitura 3.000 derruba as 1.803 que já tinham dado
certo — e a próxima execução refaz as 3.000.

**Proposta**, em ordem de valor:

1. Uma fonte composta — `from.Many([]Source)` ou equivalente — produzindo um
   único `Data`, com concorrência configurável.
2. Política de falha parcial no extract, espelhando a que o load já tem:
   continuar, e reportar no `Result` quais fontes falharam e por quê. O padrão
   pode continuar sendo abortar; o que falta é **poder escolher**.
3. `Load` aceitando um iterador, ou descarregando a cada N linhas, para que uma
   leitura longa tenha memória limitada.

---

## 3. A ordem da cadeia é propriedade de correção, e hoje é conhecimento do usuário

Numa cadeia de `Transform`, o retrato do registro cru precisa ser tirado **antes**
de qualquer campo derivado. Se inverter, o retrato sai contaminado com os campos
que a própria cadeia acabou de escrever.

Isso não dá erro. Dá um registro "cru" que não é o cru — e ninguém percebe até
alguém consultar o dado meses depois.

O extract **já tem** esse valor: o preview mostra o que a fonte mandou, e a
`v0.34.0` inclusive garantiu por teste que a cadeia não escreve nele.

**Proposta:** um `Snapshot(nome)` que capture o registro **como a fonte
entregou**, independente da posição na cadeia. Um transformer que não depende da
própria posição elimina a classe de erro em vez de documentá-la.

No mesmo espírito, e hoje um closure com type assertion repetido em todo
consumidor: um `Exigir(campos...)` que devolva `SkipRecord` quando um campo
obrigatório estiver ausente ou nulo.

---

## 4. Quem decodifica o corpo por conta própria não alcança o `PreserveNumbers`

`Source.PreserveNumbers` (v0.36.0) resolve a distinção entre `1` e `1.0` para o
decodificador **do SDK**. Mas quando o consumidor define `Records` — o caminho
para um corpo que o SDK não decodifica — quem decodifica é ele, e precisa lembrar
de `dec.UseNumber()` sozinho.

Esquecer é silencioso, e cai exatamente na chave: sem `UseNumber`, `1` e `1.0`
chegam como o mesmo `float64` e o `TextoPython` acerta um caso e erra o outro.

**Proposta:** um `Response.Decode(v any) error` que honre o `PreserveNumbers` da
própria `Source`. O `Response` já carrega o corpo e já sabe de que `Source` veio,
então a decisão fica num lugar só em vez de depender de o consumidor lembrar.

---

## 5. O `User-Agent` padrão do Go

O SDK não define `User-Agent`, então o Go envia `Go-http-client/1.1`. Todo
consumidor acaba setando um à mão.

Além da repetição: alguns provedores públicos bloqueiam ou limitam o UA padrão do
Go, e isso aparece como 403 intermitente — o tipo de falha que custa meia manhã
para diagnosticar.

**Proposta:** um padrão do próprio SDK (`brevis-sdk/<versão>`), sobreponível por
`Header`. Uma biblioteca HTTP que se identifica é higiene básica.

---

## 6. `MaxIdleConnsPerHost: 2` não serve para leitura concorrente

`newClient` (`sdk/extract/extract.go:442`) cria um `http.Client` por extract mas
**sem** `Transport` — então cai no `http.DefaultTransport`, que é singleton de
pacote com pool compartilhado. **Isso está certo**, e vale registrar para ninguém
"consertar" o que não está quebrado: não há um handshake por requisição.

Mas o `MaxIdleConnsPerHost` do transporte padrão é **2**. Sequencial, tanto faz.
Concorrente — que é exatamente o que a fonte composta do item 2 vai permitir, com
milhares de requisições ao mesmo host — tudo acima de duas conexões ociosas é
fechado e rediscado, TLS incluído.

**Proposta:** um transporte próprio com `MaxIdleConnsPerHost` proporcional à
concorrência declarada, ou um campo para ajustá-lo.

**Medir antes de implementar.** É a única forma de saber se o ganho justifica sair
do transporte padrão, e o consumidor tem um fan-out real para servir de banco de
prova quando o item 2 existir.

---

## 7. Extração incremental por `ETag` / `If-Modified-Since`

Um ETL diário relê a fonte inteira todo dia. Quando o provedor manda `ETag` ou
`Last-Modified`, uma requisição condicional devolve 304 sem corpo, e a leitura
inteira — download, decode, transform, load — pode ser pulada.

**Proposta:** requisição condicional na `Source` — guardar o validador da última
leitura no `store` que o SDK já tem (o mesmo do `Refresh`) e mandá-lo de volta; um
304 devolve um `Data` vazio com sinal claro de "não mudou".

**Registro honesto: não tenho evidência de que paga.** A fonte que motivou este
documento não manda `ETag` nem `Last-Modified` — só `cache-control: max-age=0`.
Está aqui como recurso genérico de ETL, e vale medir nas outras fontes antes de
priorizar. Se for o último da lista, é justo.

---

## 8. Compatibilidade com Python é um modo, e talvez um subpacote

`TextoPython`, `KeyPython` e `IngestionIDPython` (v0.36.0) resolveram uma classe
real, e a decisão de **não** mudar o padrão estava certa: trocá-lo reescreveria
todo id já gravado.

Do lado do consumidor, a adoção foi verificada nesta ordem, e o registro pode
servir de receita para outros:

1. teste diferencial entre a implementação local e a do SDK, sobre 15 valores e a
   faixa exponencial — concordaram em tudo, inclusive em **quando recusar**;
2. os testes de paridade contra linhas reais do warehouse seguiram passando;
3. execução contra a fonte viva: ids idênticos aos de antes do bump.

Só então a implementação local foi apagada.

**Duas coisas pendentes:**

1. **Falta o `str(x or "")`**, que é o idioma mais comum na composição de chave —
   14 dos fetchers a portar o usam. O `or ""` é a verdade-falsidade do Python:
   `None`, `""`, `0`, `0.0`, `[]`, `{}` viram `""`. Hoje isso vira ~25 linhas
   escritas à mão em cada consumidor. Proposta: `TextoPythonOuVazio(v)`.
2. **Talvez essas funções devessem viver num subpacote** (`sdk/pycompat`) em vez
   do núcleo. Elas são uma ponte para uma migração, não um conceito de ETL — e um
   subpacote diz isso na estrutura, além de manter o núcleo sem opinião sobre a
   linguagem de origem.

---

## Duas hipóteses que a medição derrubou

Ficam registradas para ninguém gastar tempo nelas.

- **Compressão.** O SDK não toca em `Accept-Encoding`, então o transporte padrão
  do Go já negocia gzip. A página medida trafega 102 KB em vez de 542 KB. Nada a
  fazer.
- **Pool de conexões.** Ver o item 6: o pool já é compartilhado. O problema não é
  o pool existir, é o tamanho dele sob concorrência.

---

## O que NÃO estou pedindo, e isso também é resposta

O fetcher que motivou este documento lê uma página HTML que embute os dados em
base64 XORado com uma chave declarada inline. O decode — regex da chave, base64,
XOR, escolher o literal mais longo — **é do consumidor**, e não generaliza.

O SDK acertou em entregar o corpo cru via `Records` e sair do caminho. Um SDK que
tentasse abraçar isso viraria um catálogo de formatos de fornecedor.

A fronteira importa tanto quanto os pedidos.

---

## Decisão do time do SDK

O documento pede que o SDK decida o que aceita, e o teste que ele propõe — *"um
time sem relação nenhuma com quem escreveu isto iria querer?"* — foi aplicado a
cada item.

| # | decisão | onde |
|---|---|---|
| 1 | **aceito**, com ressalva sobre os nomes das colunas | `v0.38.0` |
| 2 | **aceito**, é o maior e o mais genérico | `v0.39.0` |
| 3 | **aceito, com desvio** | `v0.38.0` |
| 4 | **aceito, com desvio** | `v0.37.0` |
| 5 | **aceito, com desvio** | `v0.37.0` |
| 6 | **adiado** — depende do 2 e o próprio documento pede medir antes | — |
| 7 | **adiado** — o próprio documento diz não ter evidência | — |
| 8 | **aceito, e mais fundo** | `v0.37.0` |

### Os desvios, e por quê

**Item 4 — não entrou um `Response.Decode`.** O `JSON` e o `Object`, que o
consumidor já chama, passaram a honrar o `PreserveNumbers`. Um terceiro método
que decodifica diferente dos dois existentes seria uma armadilha para quem chama
o errado — e quem chama o errado não recebe erro nenhum: recebe uma chave
diferente, que é exatamente o defeito que o item descreve.

**Item 5 — o UA não carrega a versão.** Ela viria de um const que envelhece a
cada release e que ninguém lembra de subir, e um UA que MENTE a versão é pior
que um que não a diz. Quem precisa dela põe no próprio `Header`.

**Item 8 — o seam ficou genérico, e é mais do que foi pedido.** Em vez de
`KeyPython`/`IngestionIDPython`, entraram `KeyWith`/`IngestionIDWith`, que
aceitam qualquer `Renderer`. O `pycompat` é a implementação que o SDK traz, não
a única possível — um port de Ruby ou de Scala usa a mesma porta. Isso passa
melhor no teste do próprio documento: "compatibilidade com uma linguagem" não é
conceito de ETL, mas "casar com a identidade de um sistema que já gravou" é.

O subpacote foi feito junto, e não "talvez": a estrutura diz o que a prosa
dizia.

**Item 3 — o `Snapshot` não é um transformer.** Como transformer ele dependeria
da POSIÇÃO na cadeia, que é exatamente a classe de erro que o item descreve. Ele
foi para `Source.Snapshot`, onde a garantia é estrutural: tirado onde o registro
sai da fonte, não há ordem que possa contaminá-lo.

E o `Exigir` virou `SkipWithout`, porque `RequireFields` já existe e recusa a
RESPOSTA inteira. Dois nomes parecidos para níveis diferentes é a armadilha que
o próprio SDK já encontrou em si mesmo.

### As ressalvas do item 1

O namespace configurável é aceito. **Os nomes das colunas, não como estão
propostos** — `ColumnIngestionID` não é só uma constante de nome: ele aparece no
`ON CONFLICT`, no `MERGE ... ON`, no `metadataSchema` que sobrepõe o tipo, no
índice único que os drivers SQL exigem e no padrão de particionamento. Torná-los
configuráveis é fiar essa configuração por cinco caminhos, e o ganho é uma
convenção de nome.

Fica como sobreponível **no que é declaração** (o `Schema`, que já aceita
qualquer nome) e fixo no que é mecanismo. Se um time precisar de outro nome na
coluna de dedup, isso volta como pedido com o caso concreto.

### O item 6 depende do 2, e é assim que ele deve ser medido

O `MaxIdleConnsPerHost: 2` só dói sob concorrência, e a concorrência só existe
quando a fonte composta existir. Medir antes seria medir um cenário que o SDK
ainda não produz.

Quando o item 2 estiver de pé, o banco de prova é o fan-out de 4.803 origens que
o documento cita — e a decisão sai do número, não da suposição.
