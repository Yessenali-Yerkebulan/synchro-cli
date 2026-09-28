// Command synchro is a team of AI agents in your terminal.
//
// It needs no account, no subscription and no server: all state lives in
// ~/.synchro as plain JSON, and a local Ollama install is enough to run it.
package main

import (
	"os"

	"github.com/synchro/synchro-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
