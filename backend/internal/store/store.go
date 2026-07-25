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

// ErrNotFound is returned when an external contact or group id does not exist.
var ErrNotFound = errors.New("contact not found")

// ErrExists is returned when a member is already in the group (or is the owner).
var ErrExists = errors.New("already a member")

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

// GroupMember is one non-owner participant of a personal group. An internal member is a holistic
// user referenced by username (identity resolved live from profile/instance — never copied here).
// An external member is referenced by their lowercased email: the email IS the identity, so the
// member stays resolvable for every reader without pointing at any one user's private address book.
// Name is a non-authoritative display snapshot for external members only. Roles are "admin"|"member"
// — the owner is NOT a member row (it lives in groupData.Owner, the single ownership pointer).
type GroupMember struct {
	Kind  string `json:"kind"`           // "internal" | "external"
	Ref   string `json:"ref"`            // internal: username; external: lowercased email
	Name  string `json:"name,omitempty"` // external members only: display label snapshot
	Role  string `json:"role"`           // "admin" | "member"
	Added int64  `json:"added"`
}

// Group is one personal ("contax-level") contact group: a WhatsApp-style grouping a user owns.
// It is state contax OWNS, keyed by id in state.Groups. Owner is the canonical, permanent owner
// (transfer rewrites it); Members holds only the non-owner rows. Reads return deep copies, so
// callers never alias internal state. The service layer composes the richer GroupView from this.
type Group struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Owner   string        `json:"owner"`
	Members []GroupMember `json:"members"`
	Created int64         `json:"created"`
	Updated int64         `json:"updated"`
}

type state struct {
	Users  map[string]userData `json:"users"`
	Groups map[string]Group    `json:"groups"` // personal contact groups, keyed by id
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
	s := &Store{path: path, st: state{Users: map[string]userData{}, Groups: map[string]Group{}}}
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
	// Back-compat: files written before personal groups have no "groups" key.
	if st.Groups == nil {
		st.Groups = map[string]Group{}
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

// --- personal groups (contax-owned; the daemon is the sole writer, same as everything above) ---

// ListGroupsFor returns deep copies of every group username owns or is an internal member of.
func (s *Store) ListGroupsFor(username string) []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Group
	for _, g := range s.st.Groups {
		if g.Owner == username || memberIndex(g.Members, "internal", username) >= 0 {
			out = append(out, cloneGroup(g))
		}
	}
	return out
}

// GetGroup returns a deep copy of one group. The bool reports existence.
func (s *Store) GetGroup(id string) (Group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.st.Groups[id]
	if !ok {
		return Group{}, false
	}
	return cloneGroup(g), true
}

// CreateGroup persists a new, empty group owned by owner and returns a copy.
func (s *Store) CreateGroup(owner, name string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	g := Group{ID: s.newGroupID(), Name: name, Owner: owner, Members: []GroupMember{}, Created: now, Updated: now}
	s.st.Groups[g.ID] = g
	if err := s.save(); err != nil {
		delete(s.st.Groups, g.ID)
		return Group{}, err
	}
	return cloneGroup(g), nil
}

// DeleteGroup removes a group. ErrNotFound if absent.
func (s *Store) DeleteGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.st.Groups[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.st.Groups, id)
	if err := s.save(); err != nil {
		s.st.Groups[id] = g
		return err
	}
	return nil
}

// mutateGroup applies fn to a deep copy of group id, bumps Updated, persists atomically, and
// rolls back on save failure. fn may return a domain error to abort the mutation untouched.
func (s *Store) mutateGroup(id string, fn func(g *Group) error) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.st.Groups[id]
	if !ok {
		return Group{}, ErrNotFound
	}
	next := cloneGroup(cur)
	if err := fn(&next); err != nil {
		return Group{}, err
	}
	next.Updated = time.Now().Unix()
	s.st.Groups[id] = next
	if err := s.save(); err != nil {
		s.st.Groups[id] = cur
		return Group{}, err
	}
	return cloneGroup(next), nil
}

// RenameGroup replaces a group's name.
func (s *Store) RenameGroup(id, name string) (Group, error) {
	return s.mutateGroup(id, func(g *Group) error {
		g.Name = name
		return nil
	})
}

// AddMember appends a member. ErrExists if the ref is already a member or is the owner.
func (s *Store) AddMember(id string, m GroupMember) (Group, error) {
	return s.mutateGroup(id, func(g *Group) error {
		if m.Kind == "internal" && g.Owner == m.Ref {
			return ErrExists
		}
		if memberIndex(g.Members, m.Kind, m.Ref) >= 0 {
			return ErrExists
		}
		m.Added = time.Now().Unix()
		g.Members = append(g.Members, m)
		return nil
	})
}

// RemoveMember drops a member by kind+ref. ErrNotFound if absent.
func (s *Store) RemoveMember(id, kind, ref string) (Group, error) {
	return s.mutateGroup(id, func(g *Group) error {
		i := memberIndex(g.Members, kind, ref)
		if i < 0 {
			return ErrNotFound
		}
		g.Members = append(g.Members[:i], g.Members[i+1:]...)
		return nil
	})
}

// SetMemberRole updates a member's role. ErrNotFound if absent.
func (s *Store) SetMemberRole(id, kind, ref, role string) (Group, error) {
	return s.mutateGroup(id, func(g *Group) error {
		i := memberIndex(g.Members, kind, ref)
		if i < 0 {
			return ErrNotFound
		}
		g.Members[i].Role = role
		return nil
	})
}

// SetOwner transfers ownership to an existing internal member: the new owner is removed from
// Members and the previous owner is demoted into Members as an admin. ErrNotFound if newOwner
// is not already an internal member.
func (s *Store) SetOwner(id, newOwner string) (Group, error) {
	return s.mutateGroup(id, func(g *Group) error {
		i := memberIndex(g.Members, "internal", newOwner)
		if i < 0 {
			return ErrNotFound
		}
		prev := g.Owner
		g.Members = append(g.Members[:i], g.Members[i+1:]...)
		g.Members = append(g.Members, GroupMember{Kind: "internal", Ref: prev, Role: "admin", Added: time.Now().Unix()})
		g.Owner = newOwner
		return nil
	})
}

// newGroupID returns a fresh, unused group id ("grp-" + 8 hex chars). Caller holds s.mu.
func (s *Store) newGroupID() string {
	for {
		id := "grp-" + newID()[:8]
		if _, exists := s.st.Groups[id]; !exists {
			return id
		}
	}
}

// memberIndex returns the position of the (kind, ref) member in members, or -1.
func memberIndex(members []GroupMember, kind, ref string) int {
	for i := range members {
		if members[i].Kind == kind && members[i].Ref == ref {
			return i
		}
	}
	return -1
}

// cloneGroup deep-copies a Group so callers and stored state never share the Members backing array.
func cloneGroup(g Group) Group {
	g.Members = append([]GroupMember(nil), g.Members...)
	return g
}

// newID returns a short, unguessable id for an external contact.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
