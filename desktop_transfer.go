package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var desktopSSHHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@:\[\]-]*$`)

func sendDesktopUpload(host, path string) (string, error) {
	return sendDesktopUploadContext(context.Background(), host, path)
}

func sendDesktopUploadContext(parent context.Context, host, path string) (string, error) {
	if !desktopSSHHost.MatchString(host) {
		return "", errors.New("invalid SSH host")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > uploadMaxFile {
		return "", errors.New("choose a regular file of 1 byte to 20 MiB")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(f, uploadMaxFile+1)); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	header, err := json.Marshal(desktopUploadHeader{Name: filepath.Base(path), Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil))})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	// Use the user's SSH configuration and keys. No password prompts while another
	// terminal owns stdin; host-key checking keeps the user's normal SSH policy.
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", host, "ptln _upload")
	cmd.Stdin = io.MultiReader(bytes.NewReader(append(header, '\n')), io.LimitReader(f, info.Size()+1))
	var reply, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &reply, &diagnostic
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("upload failed; check SSH access and install this ptln version on the host (%v)", err)
	}
	var result struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(reply.Bytes(), &result) != nil || !filepath.IsAbs(result.Path) || strings.ContainsAny(result.Path, "\x00\r\n") {
		return "", errors.New("host returned an invalid upload path")
	}
	return result.Path, nil
}

func desktopUploadMain(args []string) {
	if len(args) < 2 {
		fatal(errors.New("usage: ptln upload <SSH-host> <file>..."))
	}
	for _, file := range args[1:] {
		path, err := sendDesktopUpload(args[0], file)
		if err != nil {
			fatal(err)
		}
		fmt.Println(path)
	}
}

func quoteDesktopPath(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'" }
