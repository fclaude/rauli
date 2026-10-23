// Command readpage opens a web address with Raulí's tools, reads the page as
// text, follows the first link on it and takes a screenshot.
//
//	go run ./docs/examples/readpage https://example.com out.png
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

	"github.com/fclaude/rauli/internal/tools"
)

var firstRef = regexp.MustCompile(`link .*\[ref=(e\d+)\]`)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: readpage ADDRESS SCREENSHOT.png")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	s, err := tools.New(ctx, tools.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	call := func(name string, args map[string]any) tools.Result {
		raw, _ := json.Marshal(args)
		r, err := s.Call(ctx, name, raw)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		return r
	}

	page := call("navigate", map[string]any{"url": os.Args[1]})
	fmt.Println(page.Text)

	if m := firstRef.FindStringSubmatch(page.Text); m != nil {
		fmt.Println("\n--- clicking", m[1])
		fmt.Println(call("click", map[string]any{"ref": m[1]}).Text)
	}

	shot := call("screenshot", nil)
	if err := os.WriteFile(os.Args[2], shot.Images[0].Data, 0o600); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n" + shot.Text)
}
