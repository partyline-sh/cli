// Package relayserver implements the same opaque transport as the Cloudflare relay.
// It never has the Noise keys used by a phone and a computer.
package relayserver

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	_ "modernc.org/sqlite"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var secretPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Digest(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }
func randomSecret() string   { return hex.EncodeToString(randomBytes(32)) }
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func openStore(path, initialID, initialHash string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("relay data directory must be private (0700), not a symlink")
	}
	// Never follow a substituted database symlink or expose registry credentials.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("relay database must be a regular private file (0600)")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS machines(id TEXT PRIMARY KEY, hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS invites(hash TEXT PRIMARY KEY, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS device_invites(hash TEXT PRIMARY KEY, machine TEXT NOT NULL, route TEXT NOT NULL UNIQUE, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS devices(machine TEXT NOT NULL, phone TEXT NOT NULL, hash TEXT NOT NULL, PRIMARY KEY(machine,phone));
CREATE TABLE IF NOT EXISTS incoming(day TEXT PRIMARY KEY,count INTEGER NOT NULL);`)
	if err == nil && (initialID != "" || initialHash != "") {
		if !idPattern.MatchString(initialID) || !secretPattern.MatchString(initialHash) {
			err = errors.New("invalid bootstrap identity")
		} else {
			_, err = db.Exec("INSERT OR IGNORE INTO machines(id,hash) VALUES(?,?)", initialID, initialHash)
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Server) authenticated(id, token string) bool {
	if !idPattern.MatchString(id) || !secretPattern.MatchString(token) {
		return false
	}
	var found string
	return s.db.QueryRow("SELECT id FROM machines WHERE id=? AND hash=?", id, Digest(token)).Scan(&found) == nil
}

func (s *Server) join(id, invite string) (string, error) {
	if !idPattern.MatchString(id) || !secretPattern.MatchString(invite) {
		return "", errors.New("refused")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var hash string
	if err = tx.QueryRow("DELETE FROM invites WHERE hash=? AND expires>? RETURNING hash", Digest(invite), s.now().UnixMilli()).Scan(&hash); err != nil {
		return "", err
	}
	token := randomSecret()
	if _, err = tx.Exec("INSERT INTO machines(id,hash) VALUES(?,?)", id, Digest(token)); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Server) purge() {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM invites WHERE expires<=?", s.now().UnixMilli()); err != nil {
		return
	}
	if _, err = tx.Exec("DELETE FROM device_invites WHERE expires<=?", s.now().UnixMilli()); err != nil {
		return
	}
	if _, err = tx.Exec("DELETE FROM incoming WHERE day<?", s.now().Add(-48*time.Hour).UTC().Format("2006-01-02")); err != nil {
		return
	}
	_ = tx.Commit()
}

var errInviteLimit = errors.New("invite limit")

func (s *Server) issueInvite(machine, hash, route string) (string, int64, error) {
	// Expiry cleanup, quota check and insertion share one durable transaction.
	// FULL-synchronous WAL avoids DELETE-journal directory flushes without
	// allowing acknowledged revocations to disappear after a power failure.
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	table := "invites"
	if machine != "" {
		table = "device_invites"
	}
	if _, err = tx.Exec("DELETE FROM "+table+" WHERE expires<=?", s.now().UnixMilli()); err != nil {
		return "", 0, err
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		return "", 0, err
	}
	if count >= 256 {
		return "", 0, errInviteLimit
	}
	expires := s.now().Add(10 * time.Minute).UnixMilli()
	invite := ""
	if machine == "" {
		invite = randomSecret()
		_, err = tx.Exec("INSERT INTO invites(hash,expires) VALUES(?,?)", Digest(invite), expires)
	} else {
		_, err = tx.Exec("INSERT INTO device_invites(hash,machine,route,expires) VALUES(?,?,?,?)", hash, machine, route, expires)
	}
	if err != nil {
		return "", 0, err
	}
	if err = tx.Commit(); err != nil {
		return "", 0, err
	}
	return invite, expires, nil
}
