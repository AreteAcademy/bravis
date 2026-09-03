## O que muda

<!-- Uma frase. O diff mostra o quê; aqui diga o porquê. -->

## Por que

<!-- O problema que isso resolve. Se corrige bug, o que estava errado e desde quando. -->

## Como provar

<!--
Que teste falharia sem esta mudança? Para correção de bug, o teste é a prova
de que o bug existia — não "adicionei testes", mas qual comportamento eles
travam.
-->

## Checklist

- [ ] `go test ./... -race` passa
- [ ] `golangci-lint run` sem achados
- [ ] Nenhum campo público novo sem implementação
- [ ] Documentação atualizada se o comportamento mudou
- [ ] `CHANGELOG.md` atualizado se afeta o SDK público
