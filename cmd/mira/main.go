// mira indexa um repositório e responde perguntas de navegação por
// símbolo, devolvendo trechos por range de linhas.
package main

import (
	"os"

	"github.com/JonathanSantos/mira/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
