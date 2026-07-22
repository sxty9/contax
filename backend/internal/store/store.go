// Package store is contax's persistence layer for the only state contax OWNS: each user's
// externally-added contacts and their per-user "hidden" set for internal contacts. Internal
// contacts themselves are NOT stored here — they are computed live from Linux group overlap
// (visibility) and the holistic profile store (attributes), so contax never keeps a parallel
// copy of identity. This is a single flat JSON file written atomically (temp + rename), an
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

// GroupMember references one member of a personal contact group by the entity it already is —
// never a copy of its identity. An internal member is a holistic username; an external member is
// the id of one of the owner's own external contacts. The member's attributes (name, address,
// avatar) are always resolved live from that single source, exactly like an internal contact.
type GroupMember struct {
	Kind string `json:"kind"` // "internal" | "external"
	Ref  string `json:"ref"`  // internal: username; external: external contact id
}

// ContactGroup is a personal, owner-curated set of contacts — the entity contax owns and that the
// shared SDK ContactPicker and sibling services (e.g. hosuto server grants) reference by id. It
// holds only member REFERENCES; no contact identity is duplicated here.
type ContactGroup struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Members []GroupMember `json:"members"`
	Created int64         `json:"created"`
}

// userData is one owner's slice of the store: their external contacts, the usernames of the
// internal contacts they have chosen to hide (internal contacts can only be hidden, never deleted),
// and their personal contact groups.
type userData struct {
	External       []ExternalContact `json:"external"`
	HiddenInternal []string          `json:"hiddenInternal"`
	Groups         []ContactGroup    `json:"groups"`
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

// save writes the current state atomically. The caller must hold s.mu.
func (s *Store) save() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
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

// --- personal contact groups (contax's own state) ---

// ListGroups returns a deep copy of the owner's personal contact groups (creation order).
func (s *Store) ListGroups(owner string) []ContactGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneGroups(s.st.Users[owner].Groups)
}

// GetGroup returns one of the owner's groups by id. ok is false if absent.
func (s *Store) GetGroup(owner, id string) (ContactGroup, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := groupIndex(s.st.Users[owner].Groups, id); i >= 0 {
		return cloneGroup(s.st.Users[owner].Groups[i]), true
	}
	return ContactGroup{}, false
}

// FindGroup locates a group by its globally-unique id across all owners — the entry point for the
// machine-to-machine members endpoint, which knows the group id but not who owns it.
func (s *Store) FindGroup(id string) (owner string, g ContactGroup, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for o, ud := range s.st.Users {
		if i := groupIndex(ud.Groups, id); i >= 0 {
			return o, cloneGroup(ud.Groups[i]), true
		}
	}
	return "", ContactGroup{}, false
}

// CreateGroup adds a new, empty personal group for the owner and returns it.
func (s *Store) CreateGroup(owner, name string) (ContactGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := ContactGroup{ID: newGroupID(), Name: name, Created: time.Now().Unix()}
	next := append(append([]ContactGroup{}, s.st.Users[owner].Groups...), g)
	if err := s.putGroups(owner, next); err != nil {
		return ContactGroup{}, err
	}
	return g, nil
}

// RenameGroup replaces a group's name. ErrNotFound if absent.
func (s *Store) RenameGroup(owner, id, name string) (ContactGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]ContactGroup{}, s.st.Users[owner].Groups...)
	i := groupIndex(next, id)
	if i < 0 {
		return ContactGroup{}, ErrNotFound
	}
	next[i].Name = name
	if err := s.putGroups(owner, next); err != nil {
		return ContactGroup{}, err
	}
	return cloneGroup(next[i]), nil
}

// DeleteGroup removes one personal group. ErrNotFound if absent.
func (s *Store) DeleteGroup(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.st.Users[owner].Groups
	next := make([]ContactGroup, 0, len(src))
	found := false
	for _, g := range src {
		if g.ID == id {
			found = true
			continue
		}
		next = append(next, g)
	}
	if !found {
		return ErrNotFound
	}
	return s.putGroups(owner, next)
}

// AddGroupMember adds a member reference to a group, idempotent on (kind, ref). ErrNotFound if the
// group is absent. The caller is responsible for validating that the reference is one the owner may
// address (an internal user they can see, or one of their own external contacts).
func (s *Store) AddGroupMember(owner, id string, m GroupMember) (ContactGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]ContactGroup{}, s.st.Users[owner].Groups...)
	i := groupIndex(next, id)
	if i < 0 {
		return ContactGroup{}, ErrNotFound
	}
	members := append([]GroupMember{}, next[i].Members...)
	for _, e := range members {
		if e.Kind == m.Kind && e.Ref == m.Ref {
			next[i].Members = members
			return cloneGroup(next[i]), nil // already a member
		}
	}
	next[i].Members = append(members, m)
	if err := s.putGroups(owner, next); err != nil {
		return ContactGroup{}, err
	}
	return cloneGroup(next[i]), nil
}

// RemoveGroupMember drops every member whose ref matches (kinds share one id space in practice, and
// a stale ref simply matches nothing). ErrNotFound if the group is absent.
func (s *Store) RemoveGroupMember(owner, id, ref string) (ContactGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]ContactGroup{}, s.st.Users[owner].Groups...)
	i := groupIndex(next, id)
	if i < 0 {
		return ContactGroup{}, ErrNotFound
	}
	kept := make([]GroupMember, 0, len(next[i].Members))
	for _, e := range next[i].Members {
		if e.Ref == ref {
			continue
		}
		kept = append(kept, e)
	}
	next[i].Members = kept
	if err := s.putGroups(owner, next); err != nil {
		return ContactGroup{}, err
	}
	return cloneGroup(next[i]), nil
}

// putGroups swaps in the owner's new group slice and persists atomically, rolling back the
// in-memory change if the write fails. The caller must hold s.mu.
func (s *Store) putGroups(owner string, next []ContactGroup) error {
	ud := s.st.Users[owner]
	prev := ud.Groups
	ud.Groups = next
	s.st.Users[owner] = ud
	if err := s.save(); err != nil {
		ud.Groups = prev
		s.st.Users[owner] = ud
		return err
	}
	return nil
}

func groupIndex(gs []ContactGroup, id string) int {
	for i := range gs {
		if gs[i].ID == id {
			return i
		}
	}
	return -1
}

func cloneGroup(g ContactGroup) ContactGroup {
	g.Members = append([]GroupMember{}, g.Members...)
	return g
}

func cloneGroups(src []ContactGroup) []ContactGroup {
	out := make([]ContactGroup, len(src))
	for i, g := range src {
		out[i] = cloneGroup(g)
	}
	return out
}

// newGroupID returns a short, unguessable, prefixed id for a personal contact group.
func newGroupID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "grp-" + hex.EncodeToString(b[:])
}

// newID returns a short, unguessable id for an external contact.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
