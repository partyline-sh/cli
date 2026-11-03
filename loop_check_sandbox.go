package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Builder-written checks execute project code, not an authenticated engine. Never
// give them login startup files, direnv, the host keychain or a network fallback.
// Engine configuration and explicitly authorized MCP tools are a separate trust boundary.
func isolatedCheckCommand(root, command, scratch string, env []string) ([]string, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	return isolatedCheckCommandAt(root, command, scratch, home, env)
}

func isolatedCheckCommandAt(root, command, scratch, home string, env []string) ([]string, []string, error) {
	argv, err := checkSandboxCommand(runtime.GOOS, root, scratch, home, command)
	if err != nil {
		return nil, nil, err
	}
	env = withoutEnv(env, "BASH_ENV", "ENV", "ZDOTDIR", "DIRENV_DIR", "DIRENV_FILE", "DIRENV_DIFF", "TMPDIR", "TMP", "TEMP", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GOCACHE", "GOTMPDIR", "npm_config_cache", "PYTHONPYCACHEPREFIX")
	for key, dir := range map[string]string{"TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "GH_CONFIG_DIR": "gh", "GOCACHE": "go-cache", "GOTMPDIR": "tmp", "npm_config_cache": "npm-cache", "PYTHONPYCACHEPREFIX": "pycache"} {
		path := filepath.Join(scratch, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, nil, err
		}
		env = append(env, key+"="+path)
	}
	env = withoutEnv(env, "GOENV")
	env = append(env, "GOENV=off")
	// A missing namespace permission or invalid native profile is an infrastructure
	// failure, never a baseline test failure that later rounds may ignore.
	probe := append([]string(nil), argv...)
	probe[len(probe)-1] = "true"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	check := exec.CommandContext(ctx, probe[0], probe[1:]...)
	check.Dir, check.Env = root, env
	groupSpawn(check)
	if output, err := check.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("check sandbox unavailable: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return argv, env, nil
}

func resolvedCheckPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func insideCheckPath(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func checkSandboxCommand(system, root, scratch, home, command string) ([]string, error) {
	var err error
	root, err = resolvedCheckPath(root)
	if err != nil {
		return nil, err
	}
	scratch, err = resolvedCheckPath(scratch)
	if err != nil {
		return nil, err
	}
	home, err = resolvedCheckPath(home)
	if err != nil {
		return nil, err
	}
	if root == "/" || insideCheckPath(home, root) || insideCheckPath(root, filepath.Join(home, ".partyline")) {
		return nil, fmt.Errorf("builder checks require a project directory outside Partyline state, not a home directory or its parent")
	}
	tools := checkToolPaths(home)
	shell := []string{"/bin/sh", "-c", command}
	switch system {
	case "darwin":
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
			return nil, fmt.Errorf("builder checks require macOS sandbox-exec: %w", err)
		}
		q := strconv.Quote
		var p strings.Builder
		p.WriteString("(version 1)(allow default)(deny network*)(deny mach-lookup)(deny process-info*)(deny sysctl-read)(deny signal)(allow signal (target same-sandbox))")
		p.WriteString("(deny system-fcntl (fcntl-command 80 110))")
		// Go and other runtimes need page size and CPU features. Process argv and
		// environment sysctls remain denied, as does process-info access.
		p.WriteString(`(allow sysctl-read (sysctl-name-regex #"^hw\." #"^kern\.os"))`)
		p.WriteString("(deny file-read*)(allow file-read-metadata)")
		// dyld opens the root directory while locating its shared cache.
		p.WriteString(`(allow file-read* (literal "/"))`)
		systemPaths := []string{"/System", "/usr", "/bin", "/sbin", "/opt/homebrew", "/Library/Developer", "/Applications/Xcode.app", "/private/var/db/timezone", "/private/etc/localtime", "/private/etc/hosts", "/private/etc/passwd", "/private/etc/group", "/dev/null", "/dev/random", "/dev/urandom"}
		for _, path := range append(append(systemPaths, tools...), root, scratch) {
			fmt.Fprintf(&p, "(allow file-read* (subpath %s))", q(path))
		}
		fmt.Fprintf(&p, "(deny file-write*)(allow file-write* (subpath %s) (subpath %s) (literal \"/dev/null\"))", q(root), q(scratch))
		// A check may edit build outputs, but not install hooks or alter its own Git permissions.
		fmt.Fprintf(&p, "(deny file-write* (subpath %s))", q(filepath.Join(root, ".git")))
		return append([]string{"/usr/bin/sandbox-exec", "-p", p.String()}, shell...), nil
	case "linux":
		bwrap := ""
		for _, path := range []string{"/usr/bin/bwrap", "/bin/bwrap"} {
			if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				bwrap = path
				break
			}
		}
		if bwrap == "" {
			return nil, fmt.Errorf("builder checks require bubblewrap (bwrap); install it to run isolated checks")
		}
		argv := []string{bwrap, "--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp"}
		for _, path := range append([]string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/etc/alternatives", "/etc/ld.so.cache", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/hosts", "/etc/ssl"}, tools...) {
			if _, err := os.Stat(path); err == nil {
				argv = append(argv, "--ro-bind", path, path)
			}
		}
		argv = append(argv, "--bind", root, root, "--bind", scratch, scratch)
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			argv = append(argv, "--ro-bind", filepath.Join(root, ".git"), filepath.Join(root, ".git"))
		}
		argv = append(argv, "--chdir", root, "--")
		return append(argv, shell...), nil
	default:
		return nil, fmt.Errorf("isolated builder checks are not supported on %s; refusing to run without a sandbox", system)
	}
}

// Only installed tool trees are readable from the home directory. Config, tokens,
// SSH keys, other projects and shell startup files remain inaccessible. Caches written
// by the check live in its private scratch directory, not the person's shared caches.
func checkToolPaths(home string) []string {
	if resolved, err := resolvedCheckPath(home); err == nil {
		home = resolved
	}
	var paths []string
	for _, name := range []string{"go/pkg/mod", ".nvm/versions", ".local/share/mise/installs", ".local/share/fnm/node-versions", ".rustup/toolchains", ".cargo/bin", ".bun/bin", ".pyenv/versions"} {
		path, err := resolvedCheckPath(filepath.Join(home, name))
		if err == nil && path == filepath.Join(home, name) {
			paths = append(paths, path)
		}
	}
	// setup-go and other installers put Go outside the usual home tool trees.
	// Resolve the selected executable without running it unsandboxed, and grant
	// only that installation's files, never its parent or the rest of the home.
	if binary, err := exec.LookPath("go"); err == nil {
		if binary, err = resolvedCheckPath(binary); err == nil && filepath.Base(filepath.Dir(binary)) == "bin" {
			root := filepath.Dir(filepath.Dir(binary))
			if info, err := os.Stat(filepath.Join(root, "src", "runtime", "runtime.go")); err == nil && info.Mode().IsRegular() && safeCheckToolRoot(root, home) {
				paths = append(paths, root)
			}
		}
	}
	return paths
}

func safeCheckToolRoot(root, home string) bool {
	if root == "/" || insideCheckPath(home, root) {
		return false
	}
	for _, name := range []string{".partyline", ".ssh", ".config/gh", ".aws", ".azure", ".gnupg", ".codex", ".claude", "Library/Keychains"} {
		private := filepath.Join(home, name)
		if insideCheckPath(root, private) || insideCheckPath(private, root) {
			return false
		}
	}
	return true
}
