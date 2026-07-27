package main

import (
	"fmt"

	"github.com/jdwlabs/platform/internal/cli"
)

// version, commit, and date are overridden at build time via -ldflags (see
// cli/Makefile and cli/.goreleaser.yaml). A stale binary must never be
// mistaken for a current one, so buildVersion always surfaces commit+date
// even when version itself falls back to "dev" (e.g. a plain `go build`
// with no ldflags).
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func buildVersion() string {
	return fmt.Sprintf("%s (commit %s, built %s)", version, commit, date)
}

func main() {
	root, cleanup := cli.NewRoot(buildVersion())
	err := root.Execute()
	cleanup(err)
	cli.Exit(cli.ExitCode(err))
}
