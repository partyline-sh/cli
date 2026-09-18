package main

import (
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/engine"
)

// models_cmd.go — `ptln models [engine]`: what can this machine actually run?
//
// A model was a free-text field validated only by shape. A typo passed, rode all the way to a
// worker, and failed fifteen minutes later for a reason invisible from the board. Several engines
// can answer the question themselves, and the answer is machine-specific — it depends on which CLIs
// are installed here and which keys they hold — so it has to be asked here rather than shipped as a
// list that would be stale the week it was written.
func modelsMain(args []string) {
	only := ""
	if len(args) > 0 {
		only = strings.ToLower(strings.TrimSpace(args[0]))
		if _, ok := engine.Lookup(only); !ok {
			fmt.Fprintf(os.Stderr, "ptln models: unknown engine %q (have: %s)\n", only, strings.Join(engine.Names(), ", "))
			os.Exit(1)
		}
	}

	asked, answered := 0, 0
	for _, name := range engine.Names() {
		if only != "" && name != only {
			continue
		}
		asked++
		models := engine.ListModels(name)
		if len(models) == 0 {
			continue
		}
		answered++
		fmt.Printf("%s\n", name)
		for _, m := range models {
			fmt.Printf("  %s\n", m)
		}
	}

	// Silence would read as "no models exist", which is the opposite of true. Say which case it is:
	// most coding CLIs simply cannot enumerate, and that is not a failure of anything.
	if answered == 0 {
		if only != "" {
			fmt.Printf("%s does not list its models — set one by name.\n", only)
		} else {
			fmt.Printf("No installed engine here can list its models — set one by name.\n")
		}
		fmt.Println("The `llm` bridge can, and it reaches OpenAI-compatible endpoints and local models:")
		fmt.Println("  ptln daemon add-project <label> <dir> --engine llm")
		return
	}
	if only == "" && asked > answered {
		fmt.Printf("\n%d of %d engines can list models; the rest take a name.\n", answered, asked)
	}
}
