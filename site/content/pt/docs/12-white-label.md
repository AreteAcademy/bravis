---
title: Marca branca
description: Trocar título, frase, logo e paleta da interface por um arquivo YAML.
group: Operação
order: 12
slug: white-label
---

A interface é personalizável por um arquivo. As cores sobrescrevem as variáveis
CSS **em tempo de execução**, então trocar o tema de um cliente não recompila
nada.

## O arquivo

Copie `brand.example.yaml` para `brand.yaml` na raiz, ou aponte
`BREVIS_BRAND_FILE`:

```yaml
titulo: Dados Acme
subtitulo: Plataforma de dados

frase: |
  Medir antes de decidir.
  Decidir antes de escalar.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**Todos os campos são opcionais.** O que você não escrever mantém o padrão,
então um arquivo com apenas `titulo:` já é válido.

A quebra de linha em `frase` é preservada: ela define o ritmo da frase na barra
lateral.

## Logo

```yaml
logo: https://exemplo.com/marca.svg
```

Aceita URL (`https://` ou `http://`) ou caminho interno começando em `/`.
Ausente, usa o símbolo embutido, que acompanha a cor do tema.

:::warning Uma logo hospedada fora depende daquele host
Se ele cair, ou se o cluster não tiver saída para a internet, a imagem não
carrega. O resto da interface segue funcionando, mas conte com isso ao escolher
— o restante da UI, incluindo fontes e bundles, é servido do próprio binário
justamente para não depender da rede.
:::

## Validando

Um hexadecimal errado só apareceria quando o container subisse, e a mensagem
chegaria pelo log do pod — longe de quem editou o arquivo. Por isso existe um
subcomando:

```bash
brevis marca brand.yaml
```

```
  ok    Dados Acme · Plataforma de dados
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

Chame isso na CI da instalação e o erro volta no pull request.

:::note Ausência significa coisas diferentes
No `serve`, arquivo ausente quer dizer "usa a identidade padrão" — e a API sobe.
No `marca`, arquivo ausente é **erro**: quem pediu para validar um caminho
espera saber que ele não existe.
:::

## Comportamento no boot

Um erro de conteúdo — cor inválida, YAML quebrado — **não impede a interface de
subir**. O processo registra um aviso e usa o tema padrão. Derrubar a API por
causa de uma cor seria pior do que servi-la com o tema padrão.

```
level=WARN msg="identidade visual ignorada" arquivo=brand.yaml erro="cor invalida: #gggggg"
```

## O que não se customiza

O rodapé **"Powered by Brevis"** não vem da configuração — vem do código.

## Em Kubernetes

Monte o arquivo por ConfigMap:

```yaml
volumes:
  - name: marca
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: marca, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
