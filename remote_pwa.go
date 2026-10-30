package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func relayAssetDigests(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(b)
		out["/"+filepath.ToSlash(relative)] = hex.EncodeToString(hash[:])
		return nil
	})
	return out, err
}

func relayCheckAssets(ctx context.Context, c relayConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if len(c.Assets) == 0 {
		return fmt.Errorf("asset digests unavailable; run ptln relay deploy")
	}
	paths := make([]string, 0, len(c.Assets))
	for path := range c.Assets {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "..") {
			return fmt.Errorf("invalid asset path %q", path)
		}
		servedPath := path
		if path == "/index.html" {
			servedPath = "/"
		}
		req, err := http.NewRequestWithContext(ctx, "GET", c.URL+servedPath, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Cache-Control", "no-cache")
		res, err := relayHTTP.Do(req)
		if err != nil {
			return fmt.Errorf("asset %s: %w", path, err)
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(res.Body, 16*1024*1024+1))
		res.Body.Close()
		if readErr != nil || res.StatusCode != 200 || n > 16*1024*1024 {
			return fmt.Errorf("asset %s: unreadable (HTTP %d)", path, res.StatusCode)
		}
		if hex.EncodeToString(hash.Sum(nil)) != c.Assets[path] {
			return fmt.Errorf("asset mismatch: %s", path)
		}
	}
	return nil
}
