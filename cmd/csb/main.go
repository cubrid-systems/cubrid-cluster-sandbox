// Command csb provisions and inspects CUBRID topologies for development.
//
// The command surface is specified in docs/design/01-cli.md; the output
// contract and the exit codes are part of it, because cubrid-testkit
// provisions through this surface rather than screen-scraping it.
package main

import (
	"os"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr)) }
