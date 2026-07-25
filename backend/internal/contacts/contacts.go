// Package contacts is contax's read-model: it composes the three sources of a user's contact
// list without ever keeping a parallel copy of identity. Visibility of INTERNAL contacts comes
// from Linux group overlap (directory); their attributes come from the holistic profile store
// (profile) and their address from the canonical mail domain (instance). EXTERNAL contacts and the
// per-user hidden set are the only state contax owns (store). The result is symmetric by
// construction (shared hc_* group ⇔ mutual visibility) with a one-directional admin override.
package contacts

import (
	"errors"
	"net/url"
	"sort"
	"strings"

	"contax/internal/auth"
	"contax/internal/directory"
	"contax/internal/instance"
	"contax/internal/profile"
	"contax/internal/store"
)

// avatarProxyBase is the root-relative path of contaxd's own Gravatar proxy (see api.avatarExt).
const avatarProxyBase = "/api/services/contax/avatar/ext?email="

// ErrNotFound (re-exported from store) means an external contact id does not exist for the owner.
var ErrNotFound = store.ErrNotFound

// ErrNotAddressable means a group-member reference is not one the caller may address (not an
// internal user they can see, and not one of their own external contacts).
var ErrNotAddressable = errors.New("contact is not addressable by this user")

// Contact is one entry as the dashboard and the lookup API present it. Kind distinguishes an
// internal holistic user — a read-only mirror that can only be hidden — from an external contact,
// which is fully editable and deletable.
type Contact struct {
	Kind        string `json:"kind"`               // "internal" | "external"
	ID          string `json:"id"`                 // internal: username; external: contact id
	Username    string `json:"username,omitempty"` // internal only
	Nickname    string `json:"nickname"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	Hidden      bool   `json:"hidden"`
	Editable    bool   `json:"editable"` // external contacts only
}

// Service composes the read-model from its four sources.
type Service struct {
	dir   *directory.Directory
	prof  *profile.Resolver
	inst  *instance.Resolver
	store *store.Store
}

// New wires the sources together.
func New(dir *directory.Directory, prof *profile.Resolver, inst *instance.Resolver, st *store.Store) *Service {
	return &Service{dir: dir, prof: prof, inst: inst, store: st}
}

// List returns every contact for the caller: the internal contacts they may see, then their
// external contacts. Hidden entries are INCLUDED with Hidden=true so the UI can render a separate
// "hidden" section and offer to unhide them.
func (s *Service) List(u *auth.User) []Contact {
	out := s.internal(u)
	return append(out, s.external(u)...)
}

// Lookup backs the mail/icaly typeahead: non-hidden, addressable contacts whose name or email
// matches q (case-insensitive substring). limit <= 0 means no cap.
func (s *Service) Lookup(u *auth.User, q string, limit int) []Contact {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []Contact
	for _, c := range s.List(u) {
		if c.Hidden || strings.TrimSpace(c.Email) == "" {
			continue
		}
		if q != "" && !matches(c, q) {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *Service) internal(u *auth.User) []Contact {
	hidden := s.store.HiddenInternal(u.Username)
	mine := directory.ContactGroupsOf(u.Groups)
	var out []Contact
	for _, other := range s.dir.Members() {
		if other == u.Username {
			continue // never yourself
		}
		if !s.visible(u, mine, other) {
			continue
		}
		out = append(out, s.buildInternal(other, hidden[other]))
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// buildInternal composes one internal contact live from its single sources: the holistic profile
// store (name/avatar) and the canonical mail domain (address). Never a stored copy of identity.
func (s *Service) buildInternal(username string, hidden bool) Contact {
	p := s.prof.Load(username)
	return Contact{
		Kind:        "internal",
		ID:          username,
		Username:    username,
		Nickname:    p.Nickname,
		FirstName:   p.FirstName,
		LastName:    p.LastName,
		DisplayName: p.DisplayName(),
		Email:       s.inst.Address(username),
		AvatarURL:   p.AvatarURL,
		Hidden:      hidden,
		Editable:    false,
	}
}

// visible decides whether the caller may see `other` as an internal contact. Admins see everyone,
// but only one-directionally: a normal user runs this same test with IsAdmin=false, so they see an
// admin only when they share a contact group. Otherwise visibility is symmetric — the two users
// must share at least one hc_* contact group.
func (s *Service) visible(u *auth.User, mine map[string]bool, other string) bool {
	if u.IsAdmin {
		return true
	}
	if len(mine) == 0 {
		return false
	}
	for g := range s.dir.ContactGroups(other) {
		if mine[g] {
			return true
		}
	}
	return false
}

func (s *Service) external(u *auth.User) []Contact {
	var out []Contact
	for _, c := range s.store.ListExternal(u.Username) {
		out = append(out, toExternalContact(c))
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func toExternalContact(c store.ExternalContact) Contact {
	return Contact{
		Kind:        "external",
		ID:          c.ID,
		Nickname:    c.Nickname,
		FirstName:   c.FirstName,
		LastName:    c.LastName,
		DisplayName: externalDisplay(c),
		Email:       c.Email,
		AvatarURL:   externalAvatar(c.Email),
		Hidden:      c.Hidden,
		Editable:    true,
	}
}

// --- external-contact mutations (contax's own state; thin pass-throughs to the store) ---

// AddExternal creates a new external contact for the owner and returns its Contact view.
func (s *Service) AddExternal(owner, nickname, first, last, email string) (Contact, error) {
	c, err := s.store.AddExternal(owner, store.ExternalContact{Nickname: nickname, FirstName: first, LastName: last, Email: email})
	if err != nil {
		return Contact{}, err
	}
	return toExternalContact(c), nil
}

// UpdateExternal replaces one external contact's editable fields. ErrNotFound if absent.
func (s *Service) UpdateExternal(owner, id, nickname, first, last, email string) (Contact, error) {
	c, err := s.store.UpdateExternal(owner, id, nickname, first, last, email)
	if err != nil {
		return Contact{}, err
	}
	return toExternalContact(c), nil
}

// DeleteExternal removes one external contact. ErrNotFound if absent.
func (s *Service) DeleteExternal(owner, id string) error { return s.store.DeleteExternal(owner, id) }

// SetExternalHidden hides or unhides one external contact. ErrNotFound if absent.
func (s *Service) SetExternalHidden(owner, id string, hidden bool) error {
	return s.store.SetExternalHidden(owner, id, hidden)
}

// SetInternalHidden hides or unhides an internal contact (by username) for the owner.
func (s *Service) SetInternalHidden(owner, username string, hidden bool) error {
	return s.store.SetInternalHidden(owner, username, hidden)
}

// --- personal contact groups (contax is the single access point for this entity) ---

// GroupSummary is a personal group as the list and lookup present it — never its members inline, so
// callers stay portioned: fetch a group's members only when they open it.
type GroupSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount"`
}

// Groups returns the owner's personal contact groups, name-sorted.
func (s *Service) Groups(owner string) []GroupSummary {
	src := s.store.ListGroups(owner)
	out := make([]GroupSummary, 0, len(src))
	for _, g := range src {
		out = append(out, GroupSummary{ID: g.ID, Name: g.Name, MemberCount: len(g.Members)})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// LookupGroups returns the owner's groups whose name matches q (case-insensitive substring), for
// the ContactPicker's includeGroups mode. An empty q returns all of them.
func (s *Service) LookupGroups(owner, q string) []GroupSummary {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []GroupSummary
	for _, g := range s.Groups(owner) {
		if q == "" || strings.Contains(strings.ToLower(g.Name), q) {
			out = append(out, g)
		}
	}
	return out
}

// CreateGroup makes a new, empty personal group.
func (s *Service) CreateGroup(owner, name string) (GroupSummary, error) {
	g, err := s.store.CreateGroup(owner, name)
	if err != nil {
		return GroupSummary{}, err
	}
	return GroupSummary{ID: g.ID, Name: g.Name, MemberCount: len(g.Members)}, nil
}

// RenameGroup replaces a group's name. ErrNotFound if absent.
func (s *Service) RenameGroup(owner, id, name string) (GroupSummary, error) {
	g, err := s.store.RenameGroup(owner, id, name)
	if err != nil {
		return GroupSummary{}, err
	}
	return GroupSummary{ID: g.ID, Name: g.Name, MemberCount: len(g.Members)}, nil
}

// DeleteGroup removes a personal group. ErrNotFound if absent.
func (s *Service) DeleteGroup(owner, id string) error { return s.store.DeleteGroup(owner, id) }

// GroupMembers resolves a group's members to full Contact views (internal live from profile, external
// from the owner's own store). ok is false if the group does not exist. Members whose external
// reference has since been deleted are simply dropped — the group holds references, not copies.
func (s *Service) GroupMembers(owner, id string) ([]Contact, bool) {
	g, ok := s.store.GetGroup(owner, id)
	if !ok {
		return nil, false
	}
	exts := s.externalByID(owner)
	out := make([]Contact, 0, len(g.Members))
	for _, m := range g.Members {
		switch m.Kind {
		case "internal":
			out = append(out, s.buildInternal(m.Ref, false))
		case "external":
			if c, ok := exts[m.Ref]; ok {
				out = append(out, toExternalContact(c))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out, true
}

// InternalGroupMembers is the machine-to-machine view: the internal (holistic) usernames of a group,
// resolved from its globally-unique id, with the owner counted as a member. ok is false if the group
// does not exist. External members have no username and are excluded. This is what a sibling service
// (e.g. hosuto server grants) calls to keep a "share with this group" membership live.
func (s *Service) InternalGroupMembers(id string) ([]string, bool) {
	owner, g, ok := s.store.FindGroup(id)
	if !ok {
		return nil, false
	}
	set := map[string]bool{owner: true} // the owner is always a member of their own group
	for _, m := range g.Members {
		if m.Kind == "internal" && m.Ref != "" {
			set[m.Ref] = true
		}
	}
	out := make([]string, 0, len(set))
	for u := range set {
		out = append(out, u)
	}
	sort.Strings(out)
	return out, true
}

// AddGroupMember adds a contact to a group from what the ContactPicker yields: an internal user
// carries a username; an external contact carries only its address. Either way contax re-checks that
// the caller may actually address the member — an internal user they can currently see, or one of
// their own external contacts — so a group can never reference a stranger. ErrNotFound if the group
// is absent; ErrNotAddressable if the reference is not one the caller owns/sees.
func (s *Service) AddGroupMember(u *auth.User, id, username, email string) (GroupSummary, error) {
	var m store.GroupMember
	if uname := strings.ToLower(strings.TrimSpace(username)); uname != "" {
		if uname == u.Username {
			return GroupSummary{}, ErrNotAddressable // the owner is implicitly a member already
		}
		mine := directory.ContactGroupsOf(u.Groups)
		if !s.isMember(uname) || !s.visible(u, mine, uname) {
			return GroupSummary{}, ErrNotAddressable
		}
		m = store.GroupMember{Kind: "internal", Ref: uname}
	} else if extID, ok := s.externalIDByEmail(u.Username, email); ok {
		m = store.GroupMember{Kind: "external", Ref: extID}
	} else {
		return GroupSummary{}, ErrNotAddressable
	}
	g, err := s.store.AddGroupMember(u.Username, id, m)
	if err != nil {
		return GroupSummary{}, err
	}
	return GroupSummary{ID: g.ID, Name: g.Name, MemberCount: len(g.Members)}, nil
}

// RemoveGroupMember drops a member by its reference (an internal username or an external contact id).
// ErrNotFound if the group is absent.
func (s *Service) RemoveGroupMember(owner, id, ref string) (GroupSummary, error) {
	g, err := s.store.RemoveGroupMember(owner, id, strings.TrimSpace(ref))
	if err != nil {
		return GroupSummary{}, err
	}
	return GroupSummary{ID: g.ID, Name: g.Name, MemberCount: len(g.Members)}, nil
}

// isMember reports whether a username is a holistic-managed account (present in the directory).
func (s *Service) isMember(username string) bool {
	for _, m := range s.dir.Members() {
		if m == username {
			return true
		}
	}
	return false
}

func (s *Service) externalByID(owner string) map[string]store.ExternalContact {
	src := s.store.ListExternal(owner)
	m := make(map[string]store.ExternalContact, len(src))
	for _, c := range src {
		m[c.ID] = c
	}
	return m
}

func (s *Service) externalIDByEmail(owner, email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", false
	}
	for _, c := range s.store.ListExternal(owner) {
		if strings.ToLower(strings.TrimSpace(c.Email)) == email {
			return c.ID, true
		}
	}
	return "", false
}

func externalDisplay(c store.ExternalContact) string {
	if full := strings.TrimSpace(c.FirstName + " " + c.LastName); full != "" {
		return full
	}
	if n := strings.TrimSpace(c.Nickname); n != "" {
		return n
	}
	return strings.TrimSpace(c.Email)
}

// externalAvatar points at contaxd's Gravatar proxy; a 404 there makes the UI fall back to initials.
func externalAvatar(email string) string {
	if strings.TrimSpace(email) == "" {
		return ""
	}
	return avatarProxyBase + url.QueryEscape(strings.ToLower(strings.TrimSpace(email)))
}

func matches(c Contact, q string) bool {
	for _, f := range []string{c.FirstName, c.LastName, c.Nickname, c.DisplayName, c.Email} {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

func less(a, b Contact) bool {
	ai, bi := strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)
	if ai != bi {
		return ai < bi
	}
	return a.Email < b.Email
}
