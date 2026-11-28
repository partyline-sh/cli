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
	// git must work inside the sandbox: `go build` stamps VCS info with it and project tests call it.
	// Two things broke it on macOS: the user's ~/.gitconfig is unreadable here, which git treats as
	// fatal, and /usr/bin/git is Apple's xcrun shim, which crashes (SIGTRAP) when process-info is
	// denied. Checks get an empty git config, and on macOS the real git binary behind the shim.
	gitConfig := filepath.Join(scratch, "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0o600); err != nil {
		return nil, nil, err
	}
	env = withoutEnv(env, "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM")
	env = append(env, "GIT_CONFIG_GLOBAL="+gitConfig, "GIT_CONFIG_NOSYSTEM=1")
	// In a linked worktree git cannot read its metadata here (see isGitWorktree), so Go's VCS
	// stamping, the one thing every Go build needs from it, is turned off rather than failing.
	if isGitWorktree(root) {
		flags := "-buildvcs=false"
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok && v != "" {
				flags = v + " " + flags
			}
		}
		env = append(withoutEnv(env, "GOFLAGS"), "GOFLAGS="+flags)
	}
	if bin, err := checkGitBin(runtime.GOOS, scratch); err != nil {
		return nil, nil, err
	} else if bin != "" {
		env = prependPath(env, bin)
	}
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

// checkGitResolve finds the real git behind Apple's /usr/bin/git shim; a variable so tests can
// stand in for xcrun.
var checkGitResolve = func() (string, error) {
	// Runs before isolation, so never a PATH lookup: only the system xcrun, with a minimal
	// environment (no DEVELOPER_DIR or PATH from the caller).
	cmd := exec.Command("/usr/bin/xcrun", "--find", "git")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// checkGitBin returns a scratch directory holding a `git` symlink to the real binary, when the git
// on PATH is the macOS xcrun shim. "" means nothing to do. The real binary must live under a path
// the sandbox already lets checks read (Xcode or the command line tools); otherwise nothing changes.
func checkGitBin(system, scratch string) (string, error) {
	if system != "darwin" {
		return "", nil
	}
	found, err := exec.LookPath("git")
	if err != nil {
		return "", nil
	}
	if resolved, err := filepath.EvalSymlinks(found); err == nil {
		found = resolved
	}
	if found != "/usr/bin/git" {
		return "", nil // already a real git (Homebrew, a tool tree)
	}
	real, err := checkGitResolve()
	if err != nil || real == "" || real == "/usr/bin/git" {
		return "", nil
	}
	if !strings.HasPrefix(real, "/Applications/Xcode.app/") && !strings.HasPrefix(real, "/Library/Developer/") {
		return "", nil
	}
	bin := filepath.Join(scratch, "git-bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return "", err
	}
	link := filepath.Join(bin, "git")
	_ = os.Remove(link)
	if err := os.Symlink(real, link); err != nil {
		return "", err
	}
	return bin, nil
}

// prependPath puts dir first on PATH in env.
func prependPath(env []string, dir string) []string {
	for i, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			out := append([]string(nil), env...)
			out[i] = "PATH=" + dir + string(os.PathListSeparator) + v
			return out
		}
	}
	return append(env, "PATH="+dir)
}

// isGitWorktree reports whether root is a linked git worktree: its .git is a file pointing at
// metadata outside root. That metadata is deliberately NOT opened to checks, because the shared
// .git/config can hold repository credentials (http.extraheader, tokens in remote URLs), and git
// refuses to run when it cannot read that config.
func isGitWorktree(root string) bool {
	info, err := os.Lstat(filepath.Join(root, ".git"))
	return err == nil && info.Mode().IsRegular()
}
