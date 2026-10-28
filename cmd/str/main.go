// Command str is Starpoint: a declarative orchestration layer over the package
// managers and developer tooling already on the machine.
package main

import (
	"os"

	"github.com/hxmbl/starpoint/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
