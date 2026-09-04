# SDK — as decisões, e o que cada uma custou

**Vale para** `sdk/v0.19.0` · **Atualizado em** 2026-09-04

Cada linha aqui já foi decidida ao contrário uma vez. Este documento existe
para que a próxima sessão não desfaça uma lição paga — e para que, quando
desfizer, seja de propósito e sabendo o preço.

Formato: **a decisão**, o que se tentou antes, e o que isso custou.

---

## 1. O SDK não infere tipo de coluna

**Onde vale:** todos os destinos.

No BigQuery a criação de tabela delega a inferência ao próprio BigQuery: carrega
o lote numa tabela descartável com autodetect, lê o schema e sobrepõe **só** as
duas colunas que são do SDK. Custa um job a mais, na execução que cria a tabela.

**Por que não inferir em Go:** um `float64` do `encoding/json` viraria `FLOAT64`
numa coluna que o consumidor queria `NUMERIC`, e a inferência estaria de volta
pela porta dos fundos, justo nas colunas de dinheiro.

**Consequência assumida:** Postgres, MySQL e Redshift não têm serviço de
autodetect, então neles a tabela precisa existir ou o DDL vem em `CreateSQL`.
Isso é a decisão, não uma lacuna.

---

## 2. As colunas vêm do `Transform`; o destino é declarado em `Columns`

**Custou três reviravoltas.** A pergunta "quem produz as colunas?" mudou de
resposta na `v0.1.1` (agnóstico), na `v0.2.1` (contrato de seis colunas fixas) e
na `v0.9.0` (agnóstico de novo).

Na `v0.9.0` o SDK parou de preencher três das seis colunas e **nada do lado do
consumidor acusou** — a tabela seguiu existindo, com as colunas lá, vazias. O
sintoma chegou dias depois como erro de tipo do BigQuery, e a causa levou três
versões para ser isolada.

**A forma final:** o `Transform` compõe a linha; `Target.Columns` declara as
colunas do destino, **incluindo as duas que o SDK preenche**. Conferida contra
a linha nos dois sentidos e contra o destino real.

**Por que `Columns` não pode viver no `Transform`:** o metadado é acrescentado
no `Write`, depois de toda a cadeia. Um `Schema` na cadeia que nomeasse
`ingestion_id` falharia — foi por isso que a lista vivia incompleta, e não por
descuido de quem escrevia o fetcher.

---

## 3. `Accept` e `Columns` são duas verificações, e as duas valem

| | pergunta | pega |
|---|---|---|
| `Accept` | a fonte ainda manda o que eu leio? | o vendor parar de mandar um campo |
| `Columns` | a linha tem as colunas da tabela? | o fetcher esquecer de compor uma |

Fundir as duas para ter "um schema só" trocaria clareza por um buraco de
detecção.

**Sobre o nome:** a etapa (a) já se chamou `Schema`, e um fetcher real acabava
com **duas linhas `sdk.Schema` querendo dizer coisas diferentes**. E não voltou
a se chamar `Only`, que estaria livre: o `Only` original **descartava campo
ausente em silêncio**, e devolver o mesmo nome com a semântica invertida é a
troca silenciosa que a `v0.9.0` custou caro.

---

## 4. O driver é um valor, em subpacotes

**Não é um enum.** `Source.Driver string` existiu e não despachava nada — era
uma validação que recusava tudo menos o único driver implementado. Foi removido
na `v0.19.0`, junto com `DriverHTTP` e `DriverBigQuery`, porque ninguém os lia.

**Três razões, em ordem de peso:**

1. **Poda de dependência.** Go poda por pacote importado, nunca por campo usado.
   Antes da fase 0 eram 458 pacotes e 21 MB para quem só importava o SDK, porque
   a raiz importava `sdk/load` e ele importa BigQuery, Arrow e Thrift. Hoje:
   190 na raiz, 197 com `from`, e o binário caiu para 9,1 MB.
2. **Colisão de nome.** `Postgres` existe nos dois lados com configuração
   diferente. Um tipo só com os dois conjuntos de campos traz de volta o campo
   morto.
3. **Erro de compilação.** Não existe mais campo onde escrever um driver que não
   existe.

**Consequência assumida:** `Records` voltou para `from.HTTP` na `v0.19.0`,
desfazendo parte da `v0.18.0`. Lá ele subira para `Pipeline` porque `Source` era
config e ele não era; com o driver sendo um valor, `from.HTTP` **é** a origem
HTTP inteira, e um `Pipeline.Records` seria campo morto para `from.Postgres`.

---

## 5. Um campo que não faz nada é um defeito

A classe de erro que este SDK mais achou em si mesmo. O inventário:

| o quê | como terminou |
|---|---|
| `applyLayout` escrita e nunca chamada | `CreateTable` era flag sem efeito |
| três `With*` sem re-export | inalcançáveis de fora do módulo |
| `MetadataNamespace` aceito, validado, ignorado | removido |
| `SourceKeyField` declarado, zero leituras | removido |
| `Result.Pages` e `Attempts` sempre zero | ligados ao `Stats` |
| `DeleteAfterLoad` documentado "default: true" | um `bool` não consegue; virou `KeepStagedFile` |
| `core.ExtractOption` sem opção, sem consumidor, sem re-export | removido |
| `Source.Driver`, `DriverHTTP`, `DriverBigQuery` | removidos na `v0.19.0` |

**A contramedida que funciona:** provas de consumidor escritas **de fora do
módulo**, em `examples/consumer/`. Elas compilam contra a árvore de trabalho e
rodam na CI, então uma quebra de superfície aparece antes de virar release. Três
defeitos passaram por testes que viviam dentro do pacote e provavam o que o
autor enxergava.

E, quando um campo depende de outro, **recuse em vez de ignorar**: `AutoID`
junto de `Provider`/`Key` é erro nomeando os campos, porque seriam escritos e
nunca lidos.

---

## 6. Uma verificação que não pode falhar é pior que nenhuma

`verify-publication` montava URLs de proxy à mão, sem codificação de maiúsculas,
sempre dava 404 e terminava com `exit 0`. **Passou verde por versões.**

Desde então: verifique que o teste **morde**. Reverta a correção e confirme que
ele falha, antes de dar por bom. Foi assim que se achou que o teste do MERGE
posicional realmente pegava o defeito, e que a contagem de bytes cobria os dois
caminhos de leitura.

O mesmo vale para testes de poda: o caso "quem importa `to` recebe o BigQuery" é
o controle, sem o qual o teste passaria com um SDK que não carrega nada.

---

## 7. SQL montado dentro de um método com cliente nunca foi visto por um teste

O `MERGE` do BigQuery ficou **três versões** com `INSERT ROW`, que casa colunas
por **posição**, sob um comentário afirmando que casava por nome. Só funcionava
porque os schemas coincidiam por acidente. Num destino de schema fixo, o
`latitude` do consumidor caiu em `ingestion_id`.

**Regra:** `mergeSQL` e `reconcile` são funções puras, testadas sob `-short`. E
crase ou aspas em **todo** identificador: `full`, `range` e `comment` são
reservadas e aparecem em coluna real.

---

## 8. Descartar dado em silêncio é o pior modo de falhar

A regra assimétrica da reconciliação, que vale em toda comparação
registro-contra-destino:

- campo no registro que o destino não tem → **erro** nomeando o campo;
- coluna no destino que o registro não traz → NULL, legítimo;
- tipo incompatível → **erro** nomeando a coluna e os dois tipos.

Some sem sinal é pior que falhar alto. A saída para quem quis mesmo descartar é
o `Without` no `Transform`, que diz isso em voz alta.

---

## 9. Zero registros é um resultado, não uma falha

Só o `200` passava; `201`, `204` e `206` derrubavam a execução com `http NNN`.
Um vendor que responde `204` numa janela vazia virava pipeline vermelho.

Hoje **todo 2xx** chega ao `Records`, porque o que aqueles códigos significam é
convenção do vendor e só o fetcher sabe. Não-2xx segue como estava.

E a validação roda **por resposta**, não por registro: uma resposta de erro
carrega zero registros, então um validador por registro nunca seria chamado
sobre ela — a falha chegaria como "0 linhas", que não diz nada.

---

## 10. Recusa da fonte ≠ erro de programação

`sdk.Reject` e `errors.Is(err, sdk.ErrRejected)`. Os dois derrubam a execução,
mas um mapa nil e "o vendor mandou HTML no lugar de JSON" pedem coisas
diferentes de quem está de plantão — e reexecutar a mesma janela só resolve um.

`Response.Object()` e `JSON()` também devolvem recusa: um corpo que não é o
esperado é a fonte mandando algo que não é dado, com ou sem helper no meio.

---

## 11. O que é congelado

Mudar qualquer um destes quebra idempotência com toda carga anterior, em
silêncio:

| | valor |
|---|---|
| namespace do `ingestion_id` | `e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7` |
| chave do UUID v5 | `provider\|entity\|source_key\|record_ts` |
| separador do `Key()` | `\|` |

Conferido byte a byte contra `uuid.uuid5` do Python, porque uma linha escrita
aqui tem de casar com a que um fetcher Python escreve para o mesmo registro.

`MetadataNamespace` já existiu como opção configurável — aceita, validada,
default-ada e **ignorada**. Um contrato configurável não é contrato.

---

## 12. Publicação

**O proxy de módulos do Go é imutável.** Apagar a tag no git não despublica a
versão. A `v0.1.0` saiu com um `go.mod` fixando uma revisão inexistente e está
quebrada para sempre; o `README` avisa.

Consequências práticas:

- errou uma versão? lance a próxima. Nunca tente apagar a tag.
- `cmd/bravis-sdk` é módulo próprio e compila contra o **publicado**, então
  toda quebra de API falha aquele passo da CI uma vez, e o pin só sobe depois da
  tag existir. É para isso que ele existe — foi ele que pegou o rename do
  `ExtraMetadata` e a mudança de assinatura do `extract`.
- `pkg.go.dev` renderiza o README **da versão publicada**, não do branch. Um
  conserto de README precisa de tag para aparecer.

---

## 13. Onde a discussão continua

Três invariantes que o consumidor pediu em
[`plan/2026-09-03-sdk-schema-declarado.md`](plan/2026-09-03-sdk-schema-declarado.md)
e que **não estão implementados**:

- **I2** — o SDK ainda infere pelos tipos: `CreateTable` no BigQuery usa
  autodetect, então os *tipos* das colunas saem do dado, não de uma declaração.
- **I3** — a conferência declarado-contra-real acontece no `Load`, com o extract
  já feito. Num vendor com cota, é quota gasta para descobrir que a coluna não
  bate.
- **I4** — a partição continua escolhida pelo SDK (diária em
  `ingestion_loaded_at`), não declarada.

A `v0.18.0` entregou o objetivo mais profundo daquela spec — ler o `main.go` e
saber quais colunas a tabela tem — por um caminho diferente do que ela propõe
(`Columns` em vez de um bloco `Schema{}` com `From` por coluna). Os três acima
seguem em aberto e são decisão de produto, não técnica.
