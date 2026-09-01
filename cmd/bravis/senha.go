package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// semEco le uma linha do terminal com o eco desligado.
//
// A stdlib nao expoe controle de terminal, e a alternativa seria adicionar
// `golang.org/x/term` por uma unica chamada. Delegar ao `stty` custa um
// processo filho num comando interativo que roda uma vez por instalacao — e
// mantem a arvore de dependencias como esta.
//
// Se o `stty` nao existir, a senha e lida com eco em vez de o comando falhar:
// quem esta gerando um hash num container minimo prefere digitar a senha
// visivel a nao conseguir gerar o hash. O aviso deixa a escolha consciente.
func semEco() (string, error) {
	restaurar, err := desligarEco()
	if err != nil {
		fmt.Fprintln(os.Stderr, "\n(aviso: sem `stty`; a senha aparecera na tela)")
	} else {
		// Em qualquer saida — inclusive erro de leitura — o terminal volta ao
		// normal. Sem isto, um Ctrl-C no meio deixa o shell do operador mudo.
		defer restaurar()
	}

	linha, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(linha, "\r\n"), nil
}

func desligarEco() (func(), error) {
	anterior, err := stty("-g")
	if err != nil {
		return nil, err
	}
	if _, err := stty("-echo"); err != nil {
		return nil, err
	}
	return func() { _, _ = stty(anterior) }, nil
}

func stty(args ...string) (string, error) {
	c := exec.Command("stty", args...)
	c.Stdin = os.Stdin
	saida, err := c.Output()
	return strings.TrimSpace(string(saida)), err
}
