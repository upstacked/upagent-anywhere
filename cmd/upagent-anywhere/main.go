// Command upagent-anywhere installs and keeps up the machines that run
// upstacked-agent.
package main

import (
	"os"

	"github.com/upstacked/upagent-anywhere/internal/cli"
)

// version is set at build time with -ldflags.
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
