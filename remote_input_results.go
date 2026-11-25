package main

import "partyline.sh/partyline/internal/remote"

// The relay thread alone touches peer queues/ciphers. Workers only write this
// bounded mailbox, which is also kept through a transport reconnect.
func (s *remoteInputStore) publishLocked(route string, m remote.Message) {
	h := s.phones[route]
	if h == nil {
		return
	}
	if _, ok := h.results[m.ID]; !ok {
		return
	}
	cached := m
	cached.Type = "input_result"
	h.results[m.ID] = cached
	if s.mail == nil {
		s.mail = map[string][]remote.Message{}
	}
	pending := s.mail[route]
	for i, old := range pending {
		if old.ID == m.ID && old.Type == m.Type {
			pending = append(pending[:i], pending[i+1:]...)
			break
		}
	}
	pending = append(pending, m)
	if len(pending) > 256 {
		pending = pending[len(pending)-256:]
	}
	s.mail[route] = pending
}
func (s *remoteInputStore) drain(route string) []remote.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.mail[route]
	delete(s.mail, route)
	return out
}
