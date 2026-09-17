// Raw ssh handler for shared-shell mode: no TUI of ours — the joiner's terminal
// is wired straight to the session pty (view-only until granted).
package sshd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/logging"
	gossh "golang.org/x/crypto/ssh"

	"partyline.sh/partyline/internal/ptysess"
)

// Options controls how joiners are authenticated.
type Options struct {
	// InsecureAnyKey accepts any SSH key without identity verification — for
	// trusted offline/LAN use only. Identity = the claimed ssh username.
	InsecureAnyKey bool
	// Allow, if non-empty, is the allowlist of GitHub usernames permitted to
	// join (authorization on top of GitHub key authentication).
	Allow []string
}

const ctxVerified = "pl-verified-user"

func (o Options) allowed(user string) bool {
	if len(o.Allow) == 0 {
		return true
	}
	for _, a := range o.Allow {
		if strings.EqualFold(strings.TrimSpace(a), user) {
			return true
		}
	}
	return false
}

func rawHandler(s *ptysess.Session, opts Options) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			ptyReq, winCh, isPty := sess.Pty()
			if !isPty {
				fmt.Fprintln(sess, "ptln: a tty is required (use: ssh -t)")
				return
			}
			// identity: the GitHub-verified username (secure) or the claimed
			// username (insecure mode). Verified name was stashed at auth time.
			name, _ := sess.Context().Value(ctxVerified).(string)
			if name == "" {
				name = sess.User()
			}
			if name == "" {
				name = "guest"
			}
			// Local SSH joiners have no partyline account assertion → viewers
			// (watch-only). Driving requires a full-access partyline user.
			p := s.Attach(name, sess, ptyReq.Window.Width, ptyReq.Window.Height, false, false)
			defer s.Detach(p)

			go func() {
				for w := range winCh {
					s.Resize(p, w.Width, w.Height)
				}
			}()

			buf := make([]byte, 1024)
			for {
				n, err := sess.Read(buf)
				if n > 0 {
					if !s.HandleInput(p, buf[:n]) {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}
	}
}

// StartShell serves the shared-shell session over ssh.
func StartShell(s *ptysess.Session, addr, hostKeyPath string, opts Options) (*ssh.Server, error) {
	authOpts := []ssh.Option{
		wish.WithAddress(addr),
		wish.WithHostKeyPath(hostKeyPath),
	}

	if opts.InsecureAnyKey {
		// trusted LAN/offline: accept anyone, identity = claimed username
		authOpts = append(authOpts,
			wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool { return true }),
			wish.WithKeyboardInteractiveAuth(func(ctx ssh.Context, ch gossh.KeyboardInteractiveChallenge) bool { return true }),
		)
	} else {
		// default: prove identity via GitHub keys; no anonymous fallback
		authOpts = append(authOpts,
			wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
				user := ctx.User()
				if !opts.allowed(user) {
					return false
				}
				if !verifyGitHubKey(user, key) {
					return false
				}
				ctx.SetValue(ctxVerified, user)
				return true
			}),
		)
	}

	authOpts = append(authOpts, wish.WithMiddleware(rawHandler(s, opts), logging.Middleware()))
	srv, err := wish.NewServer(authOpts...)
	if err != nil {
		return nil, err
	}
	go func() { _ = srv.ListenAndServe() }()
	return srv, nil
}
