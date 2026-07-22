// Package store is contax's persistence layer for the only state contax OWNS: each user's
// externally-added contacts and their per-user "hidden" set for internal contacts. Internal
// contacts themselves are NOT stored here — they are computed live from Linux group overlap
// (visibility) and the holistic profile store (attributes), so contax never keeps a parallel
// copy of identity. This is a single flat JSON file written atomically (temp → fsync → rename), an
// in-memory snapshot guarded by one mutex, exactly like privleg's rights.json: the daemon is
// the only writer, so that is the whole concurrency story. A missing file means "no data yet".
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultPath is where contaxd keeps its owned state.
const DefaultPath = "/var/lib/contax/contacts.json"

// ErrNotFound is returned when an external contact id does not exist for an owner.
var ErrNotFound = errors.New("contact not found")

// ExternalContact is a contact the user added by hand — someone outside holistic. Unlike an
// internal contact it is fully editable and deletable, and its avatar is derived from the mail
// provider (Gravatar), never uploaded. Owned privately by one user.
type ExternalContact struct {
	ID        string `json:"id"`
	Nickname  string `json:"nickname"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"email"`
	Hidden    bool   `json:"hidden"`
	Created   int64  `json:"created"`
}

// userData is one owner's slice of the store: their external contacts and the usernames of the
// internal contacts they have chosen to hide (internal contacts can only be hidden, never deleted).
type userData struct {
	External       []ExternalContact `json:"external"`
	HiddenInternal []string          `json:"hiddenInternal"`
}

type state struct {
	Users map[string]userData `json:"users"`
}

// Store is the atomic, in-memory-cached persistence for the state. contaxd is the only writer.
type Store struct {
	path string
	mu   sync.Mutex
	st   state
}

// Open loads the store from path (DefaultPath if empty). A missing file is not an error.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: path, st: state{Users: map[string]userData{}}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.Users == nil {
		st.Users = map[string]userData{}
	}
	s.st = st
	return s, nil
}

// save writes the state atomically: temp file in the same dir → fsync → rename. The fsync is what
// makes the rename durable — without it a crash can land the rename while the data blocks are still
// unwritten, leaving a truncated contacts.json (an observable intermediate state). With it, a crash
// mid-write leaves the previous good state, never a partial one. The caller must hold s.mu.
func (s *Store) save() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".contacts-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeds
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ListExternal returns a copy of the owner's external contacts (stable order: creation order).
func (s *Store) ListExternal(owner string) []ExternalContact {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.st.Users[owner].External
	out := make([]ExternalContact, len(src))
	copy(out, src)
	return out
}

// AddExternal creates a new external contact with a fresh id and persists it.
func (s *Store) AddExternal(owner string, c ExternalContact) (ExternalContact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.ID, c.Created, c.Hidden = newID(), time.Now().Unix(), false
	ud := s.st.Users[owner]
	prev := ud.External
	ud.External = append(append([]ExternalContact{}, ud.External...), c)
	s.st.Users[owner] = ud
	if err := s.save(); err != nil {
		ud.External = prev
		s.st.Users[owner] = ud
		return ExternalContact{}, err
	}
	return c, nil
}

// UpdateExternal replaces the editable fields of one external contact. ErrNotFound if absent.
func (s *Store) UpdateExternal(owner, id, nickname, first, last, email string) (ExternalContact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ud := s.st.Users[owner]
	next := append([]ExternalContact{}, ud.External...)
	idx := -1
	for i := range next {
		if next[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ExternalContact{}, ErrNotFound
	}
	next[idx].Nickname, next[idx].FirstName, next[idx].LastName, next[idx].Email = nickname, first, last, email
	prev := ud.External
	ud.External = next
	s.st.Users[owner] = ud
	if err := s.save(); err != nil {
		ud.External = prev
		s.st.Users[owner] = ud
		return ExternalContact{}, err
	}
	return next[idx], nil
}

// DeleteExternal removes one external contact. ErrNotFound if absent.
func (s *Store) DeleteExternal(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ud := s.st.Users[owner]
	next := make([]ExternalContact, 0, len(ud.External))
	found := false
	for _, c := range ud.External {
		if c.ID == id {
			found = true
			continue
		}
		next = append(next, c)
	}
	if !found {
		return ErrNotFound
	}
	prev := ud.External
	ud.External = next
	s.st.Users[owner] = ud
	if err := s.save(); err != nil {
		ud.External = prev
		s.st.Users[owner] = ud
		return err
	}
	return nil
}

// SetExternalHidden flips the hidden flag of one external contact. ErrNotFound if absent.
func (s *Store) SetExternalHidden(owner, id string, hidden bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ud := s.st.Users[owner]
	next := append([]ExternalContact{}, ud.External...)
	idx := -1
	for i := range next {
		if next[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	next[idx].Hidden = hidden
	prev := ud.External
	ud.External = next
	s.st.Users[owner] = ud
	if err := s.save(); err != nil {
		ud.External = prev
		s.st.Users[owner] = ud
		return err
	}
	return nil
}

// HiddenInternal returns the set of internal usernames the owner has hidden.
func (s *Store) HiddenInternal(owner string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]bool, len(s.st.Users[owner].HiddenInternal))
	for _, u := range s.st.Users[owner].HiddenInternal {
		m[u] = true
	}
	return m
}

// SetInternalHidden hides or unhides an internal contact (by username) for the owner. Idempotent.
func (s *Store) SetInternalHidden(owner, username string, hidden bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ud := s.st.Users[owner]
	set := make(map[string]bool, len(ud.HiddenInternal))
	for _, u := range ud.HiddenInternal {
		set[u] = true
	}
	if hidden {
		set[username] = true
	} else {
		delete(set, username)
	}
	next := make([]string, 0, len(set))
	for u := range set {
		next = append(next, u)
	}
	sort.Strings(next)
	prev := ud.HiddenInternal
	ud.HiddenInternal = next
	s.st.Users[owner] = ud
	if err := s.save(); err != nil {
		ud.HiddenInternal = prev
		s.st.Users[owner] = ud
		return err
	}
	return nil
}

// newID returns a short, unguessable id for an external contact.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
