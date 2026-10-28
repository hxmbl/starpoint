// Command starpoint is the old name of str (STRM-445), kept so that
// `go install github.com/hxmbl/starpoint/cmd/starpoint@latest` and scripts
// that call starpoint keep working during the transition. It is the same
// program: everything lives in internal/cli, and this only starts it.
package main

import (
	"os"

	"github.com/hxmbl/starpoint/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
