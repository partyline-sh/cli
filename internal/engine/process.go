package engine

import (
	"path/filepath"
	"strings"
)

// Process identifies the executable (or its installed Node wrapper), not arbitrary
// arguments containing an engine name. Callers must establish foreground ancestry.
func Process(argv []string) (Spec, bool) {
	if len(argv) == 0 {
		return Spec{}, false
	}
	bin := strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe")
	args := argv[1:]
	if ProcessWrapper(argv) {
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			option := args[0]
			args = args[1:]
			if option == "--" {
				break
			}
			if option == "-e" || option == "--eval" || option == "-p" || option == "--print" || strings.HasPrefix(option, "--eval=") {
				return Spec{}, false
			}
			if option == "--require" || option == "-r" || option == "--import" || option == "--loader" || option == "--experimental-loader" || option == "--title" || option == "--inspect-port" {
				if len(args) == 0 {
					return Spec{}, false
				}
				args = args[1:]
			} else if !strings.Contains(option, "=") {
				switch option {
				case "--no-warnings", "--no-deprecation", "--enable-source-maps", "--trace-warnings", "--expose-gc", "--inspect", "--inspect-brk":
				default:
					return Spec{}, false
				}
			}
		}
		if len(args) == 0 {
			return Spec{}, false
		}
		path := filepath.ToSlash(args[0])
		bin = strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
		for _, name := range Names() {
			spec, _ := Lookup(name)
			if bin == spec.Bin || spec.ProcessModule != "" && strings.Contains(path, "/node_modules/"+spec.ProcessModule) {
				return interactiveProcess(spec, args[1:])
			}
		}
		return Spec{}, false
	}
	for _, name := range Names() {
		spec, _ := Lookup(name)
		if bin == spec.Bin {
			return interactiveProcess(spec, args)
		}
	}
	return Spec{}, false
}

func ProcessWrapper(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe") {
	case "node", "nodejs", "bun":
		return true
	}
	return false
}

func interactiveProcess(spec Spec, args []string) (Spec, bool) {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		switch spec.Name {
		case "claude", "gemini", "antigravity", "prime-agent":
			if arg == "mcp" || arg == "auth" || arg == "login" || arg == "logout" || arg == "install" || arg == "update" || arg == "doctor" || arg == "setup-token" {
				return Spec{}, false
			}
			if arg == "-p" || arg == "--print" || arg == "--prompt" || strings.HasPrefix(arg, "--prompt=") || strings.HasPrefix(arg, "--print=") {
				return Spec{}, false
			}
		case "codex":
			if arg == "exec" || arg == "review" || arg == "mcp-server" || arg == "app-server" || arg == "mcp" || arg == "login" || arg == "logout" || arg == "completion" {
				return Spec{}, false
			}
		case "opencode", "goose":
			if arg == "run" {
				return Spec{}, false
			}
		case "llm":
			if arg == "chat" {
				return spec, true
			}
		}
	}
	return spec, spec.Name != "llm"
}
