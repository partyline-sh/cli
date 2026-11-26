package main

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Parse terminal-escaped paths without evaluating shell syntax. A mixed paste
// (prose or paths absent on this computer) remains ordinary pasted text.
func desktopDropPaths(text string) []string {
	var paths []string
	var token strings.Builder
	var quote rune
	escaped := false
	flush := func() bool {
		if token.Len() == 0 {
			return true
		}
		path := token.String()
		token.Reset()
		if strings.HasPrefix(path, "file://") {
			u, err := url.Parse(path)
			if err != nil || (u.Host != "" && u.Host != "localhost") || u.RawQuery != "" || u.Fragment != "" {
				return false
			}
			path = u.Path
		}
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return false
			}
			path = filepath.Join(home, path[2:])
		}
		if !filepath.IsAbs(path) {
			return false
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		paths = append(paths, path)
		return len(paths) <= 16
	}
	for _, r := range text {
		switch {
		case escaped:
			token.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\r' || r == '\n':
			if !flush() {
				return nil
			}
		default:
			token.WriteRune(r)
		}
	}
	if escaped || quote != 0 || !flush() {
		return nil
	}
	return paths
}

// Only bracketed pastes are intercepted. Ordinary typing and control sequences
// pass unchanged; a failed upload never inserts the original unusable local path.
func forwardDesktopInput(input io.Reader, output io.Writer, upload func(string) (string, error), report func(error)) error {
	var pending []byte
	pasting := false
	buf := make([]byte, 4096)
	for {
		n, readErr := input.Read(buf)
		pending = append(pending, buf[:n]...)
		for len(pending) > 0 {
			marker := pasteStart
			if pasting {
				marker = pasteEnd
			}
			i := bytes.Index(pending, []byte(marker))
			if i < 0 {
				if pasting {
					if len(pending) > 1<<20 {
						return errors.New("paste exceeds 1 MiB; use ptln upload for file paths")
					}
					break
				}
				keep := partialSuffix(pending, marker)
				if _, err := output.Write(pending[:len(pending)-keep]); err != nil {
					return err
				}
				pending = append([]byte(nil), pending[len(pending)-keep:]...)
				break
			}
			if !pasting {
				if _, err := output.Write(pending[:i]); err != nil {
					return err
				}
				pending = pending[i+len(marker):]
				pasting = true
				continue
			}
			text := string(pending[:i])
			rest := pending[i+len(marker):]
			paths := desktopDropPaths(text)
			if len(paths) > 0 {
				// Keep one owner for terminal input. A fast Ctrl-C may already be
				// in this read after the paste, before upload starts polling stdin.
				if owner, ok := input.(interface{ unread([]byte) error }); ok {
					if err := owner.unread(rest); err != nil {
						return err
					}
					rest = nil
				}
				var remote []string
				accepted := true
				for _, path := range paths {
					if desktopSensitivePath(path) {
						report(errors.New("credential and hidden paths cannot be uploaded by dropping; use an explicit transfer if intended"))
						accepted = false
						break
					}
				}
				if accepted {
					for _, path := range paths {
						result, err := upload(path)
						if err != nil {
							report(err)
							remote = nil
							break
						}
						remote = append(remote, quoteDesktopPath(result))
					}
				}
				text = strings.Join(remote, " ")
			}
			// Refused or failed uploads insert nothing, including no empty control markers.
			if text != "" {
				if _, err := io.WriteString(output, pasteStart+text+pasteEnd); err != nil {
					return err
				}
			}
			pending = rest
			pasting = false
		}
		if readErr != nil {
			if len(pending) > 0 {
				if pasting {
					return errors.New("incomplete bracketed paste")
				}
				if _, err := output.Write(pending); err != nil {
					return err
				}
			}
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func desktopSensitivePath(path string) bool {
	if desktopRestrictedPath(path) {
		return true
	}
	// A harmless-looking link must not bypass the hidden/keychain restriction.
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && desktopRestrictedPath(resolved)
}

func desktopRestrictedPath(path string) bool {
	for _, component := range strings.Split(filepath.Clean(path), string(os.PathSeparator)) {
		if strings.HasPrefix(component, ".") || strings.EqualFold(component, "Keychains") {
			return true
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".p12", ".p8", ".pem", ".key", ".kdbx":
		return true
	}
	return false
}
