package main

// muxTargets adapts the built-in mux to the watcher interfaces (deliverSink / askTarget),
// whose SessionByKey now returns the backend-neutral sessIO. The nil guard matters: a nil
// *ptysess.Session boxed into a non-nil interface would pass the watchers' liveness checks
// and then panic on the first write.
import (
	"partyline.sh/partyline/internal/ptymux"
	"partyline.sh/partyline/internal/ptysess"
)

type muxTargets struct{ mx *ptymux.Mux }

func (t muxTargets) SessionByKey(key string) (sessIO, string, string, bool) {
	sess, label, dir, ok := t.mx.SessionByKey(key)
	if sess == nil {
		return nil, label, dir, ok
	}
	return sess, label, dir, ok
}
func (t muxTargets) UnsubmittedInput(key string) (int, bool) { return t.mx.UnsubmittedInput(key) }
func (t muxTargets) SessionStatus(key string) string         { return t.mx.SessionStatus(key) }
func (t muxTargets) SetBanner(s string)                      { t.mx.SetBanner(s) }
func (t muxTargets) LiveSessions() []ptymux.LiveSession      { return t.mx.LiveSessions() }

func (t muxTargets) ActiveLaunch() (argv []string, dir, label, key string, ok bool) {
	return t.mx.ActiveLaunch()
}

func (t muxTargets) ActiveSessionIO() (sessIO, string) {
	sess, label := t.mx.ActiveSession()
	if sess == nil {
		return nil, label
	}
	return sess, label
}

func (t muxTargets) ActiveShareable() (*ptysess.Session, string) { return t.mx.ActiveSession() }
func (t muxTargets) SetActiveShared(on bool)                     { t.mx.SetActiveShared(on) }
