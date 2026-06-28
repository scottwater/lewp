package main

import (
	"os"

	"github.com/scottwater/lewp/internal/cli"
)

func main() {
	os.Exit(cli.Run(cli.Config{Args: os.Args[1:]}))
}
