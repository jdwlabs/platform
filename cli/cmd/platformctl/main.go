package main

import (
	"github.com/jdwlabs/platform/internal/cli"
)

// version is overridden at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	root, cleanup := cli.NewRoot(version)
	err := root.Execute()
	cleanup(err)
	cli.Exit(cli.ExitCode(err))
}
