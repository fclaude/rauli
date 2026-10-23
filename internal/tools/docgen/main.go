// Command docgen prints the tool reference as Markdown. The documentation site
// builds its Tools page from this output, so what the page says is what the
// schemas say.
//
//	go run ./internal/tools/docgen            # the reference
//	go run ./internal/tools/docgen -names     # one tool name per line
package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/fclaude/rauli/internal/tools"
)

func main() {
	names := flag.Bool("names", false, "print the tool names, one per line, instead of the reference")
	flag.Parse()
	if *names {
		fmt.Println(strings.Join(tools.Names(), "\n"))
		return
	}
	fmt.Print(tools.Markdown())
}
