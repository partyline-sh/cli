package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"partyline.sh/partyline/internal/remote"
)

type remotePhone struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Static         []byte            `json:"static"`
	Credential     remote.Credential `json:"credential"`
	Fingerprint    string            `json:"fingerprint"`
	TokenHash      string            `json:"tokenHash"`
	PairedAt       time.Time         `json:"pairedAt"`
	RevokeRejected bool              `json:"revokeRejected,omitempty"`
	LastSeen       *time.Time        `json:"lastSeen"`
}

func phoneFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	s := hex.EncodeToString(sum[:16])
	groups := make([]string, 8)
	for i := range groups {
		groups[i] = s[i*4 : i*4+4]
	}
	return strings.Join(groups, " ")
}
func remotePhones() ([]remotePhone, error) {
	var phones []remotePhone
	b, err := os.ReadFile(filepath.Join(relayDir(), "phones.json"))
	if os.IsNotExist(err) {
		return phones, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(b, &phones)
	return phones, err
}
func withPhoneLock(do func() error) error {
	f, err := os.OpenFile(filepath.Join(relayDir(), "phones.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return do()
}

// Append the audit entry before replacing the phone projection. Ordinary write
// failures restore the previous journal length; callers hold phones.lock.
func writePhones(phones []remotePhone, action string, p remotePhone) (err error) {
	b, err := json.MarshalIndent(phones, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(relayDir(), "events.jsonl")
	_, statErr := os.Stat(path)
	created := os.IsNotExist(statErr)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			rollback := f.Truncate(info.Size())
			if rollback == nil {
				rollback = f.Sync()
			}
			if created && rollback == nil {
				rollback = os.Remove(path)
			}
			err = errors.Join(err, rollback)
		}
	}()
	err = json.NewEncoder(f).Encode(struct {
		At     time.Time   `json:"at"`
		Action string      `json:"action"`
		Phone  remotePhone `json:"phone"`
		Method string      `json:"method"`
	}{time.Now().UTC(), action, p, gateConfirmMethod})
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return relayPrivateFile(filepath.Join(relayDir(), "phones.json"), append(b, '\n'))
}
func approveRemotePhone(ctx context.Context, c relayConfig, token string, p remotePhone) (string, error) {
	var deviceToken string
	err := withPhoneLock(func() error {
		phones, err := remotePhones()
		if err != nil {
			return err
		}
		for _, old := range phones {
			if old.ID == p.ID || string(old.Static) == string(p.Static) {
				return errors.New("phone already paired")
			}
		}
		drop := func(cause error) error {
			var out map[string]any
			rollback := relayPost(context.WithoutCancel(ctx), c.URL, "/device-revoke", map[string]string{"id": c.MachineID, "token": token, "phone": p.ID}, &out)
			return errors.Join(cause, rollback)
		}
		var issued struct {
			Token string `json:"token"`
		}
		if err = relayPost(ctx, c.URL, "/device-token", map[string]string{"id": c.MachineID, "token": token, "phone": p.ID}, &issued); err != nil {
			// The response can be lost after the relay commits issuance.
			return drop(err)
		}
		if err = ctx.Err(); err != nil {
			return drop(err)
		}
		raw, err := hex.DecodeString(issued.Token)
		if err != nil || len(raw) != 32 {
			return drop(errors.New("invalid device token"))
		}
		digest := sha256.Sum256([]byte(issued.Token))
		p.TokenHash = hex.EncodeToString(digest[:])
		p.PairedAt = time.Now().UTC()
		if err = writePhones(append(phones, p), "paired", p); err != nil {
			return drop(err)
		}
		deviceToken = issued.Token
		return nil
	})
	return deviceToken, err
}
func revokeRemotePhone(ctx context.Context, id string) error {
	return withPhoneLock(func() error {
		phones, err := remotePhones()
		if err != nil {
			return err
		}
		for i, p := range phones {
			if p.ID != id {
				continue
			}
			if err = writePhones(append(phones[:i:i], phones[i+1:]...), "revoked", p); err != nil {
				return err
			}
			pending, queueErr := pendingRemoteRevokes()
			if queueErr == nil {
				queueErr = savePendingRemoteRevokes(append(pending, p))
			}
			if queueErr != nil {
				fmt.Fprintf(os.Stderr, "revoked locally; relay not yet told: cannot record retry: %v\n", queueErr)
				return nil
			}
			c, token, err := loadRelay()
			if err != nil {
				fmt.Fprintln(os.Stderr, "revoked locally; relay not yet told, will retry")
				return nil
			} // Local revoke succeeded; watcher will retry.
			if err := flushRemoteRevokesLocked(ctx, c, token); err != nil {
				fmt.Fprintf(os.Stderr, "revoked locally; relay not yet told, will retry: %v\n", err)
			}
			return nil
		}
		return errors.New("phone not found")
	})
}
func pendingRemoteRevokes() ([]remotePhone, error) {
	var pending []remotePhone
	b, err := os.ReadFile(filepath.Join(relayDir(), "pending-revokes.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &pending); err != nil {
		// Preserve evidence of corruption before allowing new notifications.
		if backupErr := relayPrivateFile(filepath.Join(relayDir(), "pending-revokes.corrupt.json"), b); backupErr != nil {
			return nil, errors.Join(err, backupErr)
		}
		fmt.Fprintln(os.Stderr, "invalid pending revoke queue; preserved as pending-revokes.corrupt.json")
		return nil, nil
	}
	return pending, nil
}
func savePendingRemoteRevokes(pending []remotePhone) error {
	b, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return relayPrivateFile(filepath.Join(relayDir(), "pending-revokes.json"), append(b, '\n'))
}
func flushRemoteRevokes(ctx context.Context, c relayConfig, token string) error {
	return withPhoneLock(func() error { return flushRemoteRevokesLocked(ctx, c, token) })
}
func flushRemoteRevokesLocked(ctx context.Context, c relayConfig, token string) error {
	pending, err := pendingRemoteRevokes()
	if err != nil {
		return err
	}
	var failures []error
	for i := 0; i < len(pending); {
		p := pending[i]
		if p.RevokeRejected {
			i++
			continue
		}
		// Also finish local revocation if the process stopped after writing intent.
		phones, err := remotePhones()
		if err != nil {
			return err
		}
		for i, phone := range phones {
			if phone.ID == p.ID {
				if err = writePhones(append(phones[:i:i], phones[i+1:]...), "revoked", phone); err != nil {
					return err
				}
				break
			}
		}
		var out map[string]any
		if err = relayPost(ctx, c.URL, "/device-revoke", map[string]string{"id": c.MachineID, "token": token, "phone": p.ID}, &out); err != nil {
			var status *relayHTTPError
			if errors.As(err, &status) && (status.Status == 400 || status.Status == 404 || status.Status == 422) {
				pending[i].RevokeRejected = true
				if saveErr := savePendingRemoteRevokes(pending); saveErr != nil {
					return saveErr
				}
			}
			failures = append(failures, err)
			i++
			continue
		}
		pending = append(pending[:i:i], pending[i+1:]...)
		if err = savePendingRemoteRevokes(pending); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

func printRemotePhones() error {
	pending, err := pendingRemoteRevokes()
	if err != nil {
		return err
	}
	for _, p := range pending {
		status := "revoked, relay not yet told"
		if p.RevokeRejected {
			status += " (relay rejected revoke; manual retry required)"
		}
		fmt.Printf("%s  %s  %s\n", p.ID, termSafe(p.Name), status)
	}
	phones, err := remotePhones()
	if err != nil {
		return err
	}
	for _, p := range phones {
		last := "never"
		if p.LastSeen != nil {
			last = p.LastSeen.Format(time.RFC3339)
		}
		fmt.Printf("%s  %s  %s  paired %s  last seen %s\n", p.ID, termSafe(p.Name), p.Fingerprint, p.PairedAt.Format(time.RFC3339), last)
	}
	return nil
}
