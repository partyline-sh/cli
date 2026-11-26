package gatewayjoin

import (
	"crypto/rand"
	"errors"
	"github.com/flynn/noise"
	"os"
)

func LoadKey(path string) (noise.DHKey, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key, e := noise.DH25519.GenerateKeypair(rand.Reader)
		if e != nil {
			return key, e
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return noise.DHKey{}, e
		}
		_, e = f.Write(append(key.Private, key.Public...))
		closeErr := f.Close()
		if e != nil {
			return noise.DHKey{}, e
		}
		return key, closeErr
	}
	if err != nil {
		return noise.DHKey{}, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return noise.DHKey{}, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || len(b) != 64 {
		return noise.DHKey{}, errors.New("invalid private gateway identity file")
	}
	return noise.DHKey{Private: b[:32], Public: b[32:]}, nil
}
