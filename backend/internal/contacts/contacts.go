// Package contacts is contax's read-model: it composes the three sources of a user's contact
// list without ever keeping a parallel copy of identity. Visibility of INTERNAL contacts comes
// from Linux group overlap (directory); their attributes come from the holistic profile store
// (profile) and their address from the canonical mail domain (instance). EXTERNAL contacts and the
// per-user hidden set are the only state contax owns (store). The result is symmetric by
// construction (shared hc_* group ⇔ mutual visibility) with a one-directional admin override.
package contacts

import (
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
		p := s.prof.Load(other)
		out = append(out, Contact{
			Kind:        "internal",
			ID:          other,
			Username:    other,
			Nickname:    p.Nickname,
			FirstName:   p.FirstName,
			LastName:    p.LastName,
			DisplayName: p.DisplayName(),
			Email:       s.inst.Address(other),
			AvatarURL:   p.AvatarURL,
			Hidden:      hidden[other],
			Editable:    false,
		})
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
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
