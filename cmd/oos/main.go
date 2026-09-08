// oos: out of space. See internal/cli for the command surface.
package main

import (
	"os"

	"github.com/afterdarksys/oos/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
