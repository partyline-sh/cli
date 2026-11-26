package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/engine"
)

type agentProcess struct {
	pid, parent, group, foreground int
	started                        time.Time
	argv                           []string
}

type detectedAgent struct {
	engine  string
	process agentProcess
}

func declaredShell(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "shell", "sh", "bash", "zsh", "fish", "pwsh", "powershell":
		return true
	}
	return false
}

func (a *detectedAgent) target() string {
	return fmt.Sprintf("agent:%s:%d:%d:%d", a.engine, a.process.pid, a.process.group, a.process.started.Unix())
}

// Arguments remain local; only engine and process identity leave this function.
var readAgentProcesses = func(tty string) ([]agentProcess, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-t", strings.TrimPrefix(tty, "/dev/"), "-ww", "-o", "pid=,ppid=,pgid=,tpgid=,lstart=,args=")
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseAgentProcesses(string(out)), nil
}

func parseAgentProcesses(output string) []agentProcess {
	var result []agentProcess
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		var ids [4]int
		valid := true
		for i := range ids {
			n, err := strconv.Atoi(f[i])
			if err != nil {
				valid = false
			}
			ids[i] = n
		}
		started, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[4:9], " "), time.UTC)
		if !valid || err != nil || ids[0] <= 0 {
			continue
		}
		result = append(result, agentProcess{ids[0], ids[1], ids[2], ids[3], started, f[9:]})
	}
	return result
}

// Only the foreground process group and its ancestors count. A background MCP
// server, a filename, or text mentioning an engine cannot adopt a shell.
func foregroundAgent(root int, processes []agentProcess) *detectedAgent {
	byID := map[int]agentProcess{}
	for _, p := range processes {
		byID[p.pid] = p
	}
	base, ok := byID[root]
	if !ok || base.foreground <= 0 {
		return nil
	}
	var best *detectedAgent
	bestOwner := 0
	bestDepth := int(^uint(0) >> 1)
	ambiguous := false
	for _, active := range processes {
		if active.group != base.foreground {
			continue
		}
		var path []agentProcess
		seen := map[int]bool{}
		for p := active; !seen[p.pid]; {
			seen[p.pid] = true
			path = append(path, p)
			if p.pid == root {
				for i := len(path) - 1; i >= 0; i-- {
					spec, match := engine.Process(path[i].argv)
					if !match {
						continue
					}
					depth := len(path) - 1 - i
					chosen := path[i]
					if engine.ProcessWrapper(chosen.argv) {
						for j := i - 1; j >= 0; j-- {
							child, matches := engine.Process(path[j].argv)
							if matches && child.Name == spec.Name && !engine.ProcessWrapper(path[j].argv) {
								chosen = path[j]
								break
							}
						}
					}
					if depth < bestDepth {
						best, bestDepth, bestOwner, ambiguous = &detectedAgent{spec.Name, chosen}, depth, path[i].pid, false
					} else if depth == bestDepth {
						if bestOwner != path[i].pid {
							ambiguous = true
						} else if engine.ProcessWrapper(best.process.argv) && !engine.ProcessWrapper(chosen.argv) {
							best.process = chosen
						} else if !engine.ProcessWrapper(best.process.argv) && !engine.ProcessWrapper(chosen.argv) && best.process.pid != chosen.pid {
							ambiguous = true
						}
					}
					break // an interactive parent owns its tool children
				}
				break
			}
			parent, exists := byID[p.parent]
			if !exists {
				break
			}
			p = parent
		}
	}
	if ambiguous {
		return nil
	}
	return best
}

func detectPaneAgent(pid, tty string) (*detectedAgent, error) {
	root, err := strconv.Atoi(pid)
	if err != nil || root <= 0 || !strings.HasPrefix(tty, "/dev/") {
		return nil, fmt.Errorf("invalid pane process")
	}
	processes, err := readAgentProcesses(tty)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect pane processes: %w", err)
	}
	for _, process := range processes {
		if process.pid == root {
			return foregroundAgent(root, processes), nil
		}
	}
	return nil, fmt.Errorf("pane process unavailable")
}
