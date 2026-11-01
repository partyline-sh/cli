package phoneterm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const MaxSessions = 8
const Retention = 24 * time.Hour

type Session struct {
	Fresh        bool      `json:"-"`
	ID           string    `json:"id"`
	Owner        string    `json:"owner"`
	Request      string    `json:"request"`
	Title        string    `json:"title"`
	Created      time.Time `json:"created"`
	LastAttached time.Time `json:"lastAttached"`
}

type Registry struct{ Root, Socket string }

func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == strings.ToLower(id)
}
func (r Registry) locked(fn func(*[]Session) error) error {
	if r.Socket == "" {
		return errors.New("phone tmux socket unavailable")
	}
	if err := os.MkdirAll(r.Root, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(r.Root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	path := filepath.Join(r.Root, "sessions.json")
	var sessions []Session
	if raw, e := os.ReadFile(path); e == nil {
		if err = json.Unmarshal(raw, &sessions); err != nil {
			return fmt.Errorf("phone session registry: %w", err)
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	for _, session := range sessions {
		if !validID(session.ID) {
			return errors.New("invalid phone session registry")
		}
	}
	if err = fn(&sessions); err != nil {
		return err
	}
	raw, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.Root, ".sessions-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (r Registry) List(owner string) ([]Session, error) {
	var result []Session
	err := r.locked(func(sessions *[]Session) error {
		for _, s := range *sessions {
			if owner == "" || s.Owner == owner {
				result = append(result, s)
			}
		}
		return nil
	})
	return result, err
}

func (r Registry) Create(owner, request, shell string, cols, rows int, before ...func(Session) error) (Session, error) {
	var result Session
	created := false
	if owner == "" || request == "" || len(request) > 128 {
		return result, errors.New("invalid terminal request")
	}
	if !filepath.IsAbs(shell) {
		return result, errors.New("login shell unavailable")
	}
	err := r.locked(func(sessions *[]Session) error {
		for _, s := range *sessions {
			if s.Owner == owner && s.Request == request {
				result = s
				return nil
			}
		}
		if len(*sessions) >= MaxSessions {
			return errors.New("eight phone terminals already exist; end one before creating another")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		result = Session{ID: hex.EncodeToString(nonce[:]), Owner: owner, Request: request, Title: "Phone terminal", Created: time.Now(), LastAttached: time.Now()}
		if len(before) > 0 && before[0] != nil {
			if err := before[0](result); err != nil {
				return err
			}
		}
		result.Fresh = true
		name := Prefix + result.ID
		size := dimensions(cols, rows)
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		args := []string{"new-session", "-d", "-s", name, "-c", home, "-x", fmt.Sprint(size.Cols), "-y", fmt.Sprint(size.Rows), "--", shell, "-l"}
		if out, err := command(r.Socket, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("create phone terminal: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		for _, option := range [][2]string{{"prefix", "None"}, {"prefix2", "None"}, {"status", "off"}, {"mouse", "off"}} {
			if out, err := command(r.Socket, "set-option", "-t", name, option[0], option[1]).CombinedOutput(); err != nil {
				_ = command(r.Socket, "kill-session", "-t", name).Run()
				return fmt.Errorf("configure %s: %w (%s)", option[0], err, out)
			}
		}
		if out, err := command(r.Socket, "unbind-key", "-a", "-T", "root").CombinedOutput(); err != nil {
			_ = command(r.Socket, "kill-session", "-t", name).Run()
			return fmt.Errorf("configure root keys: %w (%s)", err, out)
		}
		created = true
		*sessions = append(*sessions, result)
		return nil
	})
	if err != nil && created {
		_ = command(r.Socket, "kill-session", "-t", Prefix+result.ID).Run()
	}
	return result, err
}

func (r Registry) Attach(owner, id string, cols, rows int) (*Attachment, error) {
	var result *Attachment
	err := r.locked(func(sessions *[]Session) error {
		for i, s := range *sessions {
			if s.ID == id && s.Owner == owner {
				clients, err := command(r.Socket, "list-clients", "-t", Prefix+id, "-F", "#{client_pid}").Output()
				if err != nil {
					return errors.New("terminal session is no longer running")
				}
				if strings.TrimSpace(string(clients)) != "" {
					return errors.New("another attachment is using this terminal")
				}
				result, err = attach(r.Socket, Prefix+id, cols, rows)
				if err == nil {
					(*sessions)[i].LastAttached = time.Now()
				}
				return err
			}
		}
		return errors.New("phone terminal not found")
	})
	if err != nil && result != nil {
		result.Close()
		_ = result.Wait()
		result = nil
	}
	return result, err
}

func (r Registry) End(owner, id string) error {
	return r.locked(func(sessions *[]Session) error {
		for i, s := range *sessions {
			if s.ID == id && (owner == "" || s.Owner == owner) {
				if err := command(r.Socket, "kill-session", "-t", Prefix+id).Run(); err != nil {
					return errors.New("could not end phone terminal")
				}
				*sessions = append((*sessions)[:i], (*sessions)[i+1:]...)
				return nil
			}
		}
		return errors.New("phone terminal not found")
	})
}

// Detached starts the retention clock at detach, never at the last keystroke.
func (r Registry) Detached(owner, id string) error {
	return r.locked(func(sessions *[]Session) error {
		for i, s := range *sessions {
			if s.ID == id && s.Owner == owner {
				(*sessions)[i].LastAttached = time.Now()
				return nil
			}
		}
		return nil
	})
}

// Cleanup removes exited shells and sessions detached for longer than Retention.
// A tmux attachment prevents expiry even if it has produced no output for days.
func (r Registry) Cleanup(now time.Time) error {
	return r.locked(func(sessions *[]Session) error {
		var kept []Session
		for _, s := range *sessions {
			if err := command(r.Socket, "has-session", "-t", Prefix+s.ID).Run(); err != nil {
				continue
			}
			if now.Sub(s.LastAttached) >= Retention {
				out, err := command(r.Socket, "list-clients", "-t", Prefix+s.ID, "-F", "#{client_pid}").Output()
				if err != nil {
					return err
				}
				if strings.TrimSpace(string(out)) == "" {
					if err = command(r.Socket, "kill-session", "-t", Prefix+s.ID).Run(); err != nil {
						return err
					}
					continue
				}
			}
			kept = append(kept, s)
		}
		*sessions = kept
		return nil
	})
}
