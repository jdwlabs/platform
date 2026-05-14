package main

import (
	"github.com/jdwlabs/platform/internal/cli"
)

func main() {
	root := cli.NewRoot()
	err := root.Execute()
	cli.Exit(cli.ExitCode(err))
}
