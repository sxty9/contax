// This file adds "personal groups" — a contax-level, WhatsApp-style grouping a user OWNS — on top
// of the read-model. A group is state contax owns (in store), but its members are resolved through
// the SAME sources as every other contact: internal members via profile+instance, external members
// via the Gravatar proxy. Authorization is intra-group (owner/admin/member) and computed here from
// the session user — it is NOT a central privleg right. Adding a member reuses the read-model's
// visibility test, so a user may only add contacts they can already see.
package contacts

import (
	"errors"
	"sort"
	"strings"

	"contax/internal/auth"
	"contax/internal/directory"
	"contax/internal/store"
)

// Group-layer errors, mapped to HTTP status by the API. ErrNotFound is re-used from the store.
var (
	// ErrForbidden means the caller lacks the role required for the action (=> 403).
	ErrForbidden = errors.New("forbidden")
	// ErrInvalid means a malformed/invalid argument (=> 400).
	ErrInvalid = errors.New("invalid request")
	// ErrExists (re-exported) means the member is already in the group (=> 409).
	ErrExists = store.ErrExists
)

// Roles within a personal group. The owner is synthetic (from Group.Owner), never a member row.
const (
	roleOwner  = "owner"
	roleAdmin  = "admin"
	roleMember = "member"
)

// MemberView is one resolved participant as the dashboard presents it (owner row included).
type MemberView struct {
	Kind        string `json:"kind"` // "internal" | "external"
	Ref         string `json:"ref"`  // internal: username; external: email
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	Role        string `json:"role"` // "owner" | "admin" | "member"
}

// GroupView is the DTO the API returns: the group plus the CALLER's own role, so the UI needs no
// second call to decide which actions to offer.
type GroupView struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Owner       string       `json:"owner"`
	Role        string       `json:"role"` // caller's role: "owner" | "admin" | "member"
	Members     []MemberView `json:"members"`
	MemberCount int          `json:"memberCount"`
	Created     int64        `json:"created"`
	Updated     int64        `json:"updated"`
}

// GroupLookup is the lean group hit returned by lookup?includeGroups=1 for cross-service pickers.
type GroupLookup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount"`
}

// ListGroups returns the caller's groups (owned or member), name-sorted.
func (s *Service) ListGroups(u *auth.User) []GroupView {
	groups := s.store.ListGroupsFor(u.Username)
	out := make([]GroupView, 0, len(groups))
	for _, g := range groups {
		out = append(out, s.toGroupView(g, u.Username))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// GetGroup returns one group's view. ErrForbidden if the caller is not a member.
func (s *Service) GetGroup(u *auth.User, id string) (GroupView, error) {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if roleOf(g, u.Username) == "" {
		return GroupView{}, ErrForbidden
	}
	return s.toGroupView(g, u.Username), nil
}

// CreateGroup makes a new group owned by the caller. Any authenticated user may create one.
func (s *Service) CreateGroup(u *auth.User, name string) (GroupView, error) {
	g, err := s.store.CreateGroup(u.Username, name)
	if err != nil {
		return GroupView{}, err
	}
	return s.toGroupView(g, u.Username), nil
}

// RenameGroup renames a group (owner or admin).
func (s *Service) RenameGroup(u *auth.User, id, name string) (GroupView, error) {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if r := roleOf(g, u.Username); r != roleOwner && r != roleAdmin {
		return GroupView{}, ErrForbidden
	}
	ng, err := s.store.RenameGroup(id, name)
	if err != nil {
		return GroupView{}, err
	}
	return s.toGroupView(ng, u.Username), nil
}

// DeleteGroup deletes a group (owner only).
func (s *Service) DeleteGroup(u *auth.User, id string) error {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return ErrNotFound
	}
	if roleOf(g, u.Username) != roleOwner {
		return ErrForbidden
	}
	return s.store.DeleteGroup(id)
}

// AddMember adds an internal user or an external contact to a group (owner or admin). The candidate
// must be VISIBLE to the acting user: an internal candidate must pass the read-model visibility
// test; an external candidate's email must be one of the acting user's own external contacts.
func (s *Service) AddMember(u *auth.User, id, kind, ref string) (GroupView, error) {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if r := roleOf(g, u.Username); r != roleOwner && r != roleAdmin {
		return GroupView{}, ErrForbidden
	}
	m := store.GroupMember{Role: roleMember}
	switch kind {
	case "internal":
		name := strings.ToLower(strings.TrimSpace(ref))
		if !s.canSeeInternal(u, name) {
			return GroupView{}, ErrForbidden
		}
		m.Kind, m.Ref = "internal", name
	case "external":
		email := strings.ToLower(strings.TrimSpace(ref))
		label, ok := s.ownExternalEmail(u.Username, email)
		if !ok {
			return GroupView{}, ErrForbidden
		}
		m.Kind, m.Ref, m.Name = "external", email, label
	default:
		return GroupView{}, ErrInvalid
	}
	ng, err := s.store.AddMember(id, m)
	if err != nil {
		return GroupView{}, err
	}
	return s.toGroupView(ng, u.Username), nil
}

// RemoveMember removes a member, or lets an internal member remove THEMSELVES (leave). The owner
// cannot leave — they must transfer ownership or delete the group.
func (s *Service) RemoveMember(u *auth.User, id, kind, ref string) error {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return ErrNotFound
	}
	callerRole := roleOf(g, u.Username)
	if callerRole == "" {
		return ErrForbidden
	}
	kind = strings.TrimSpace(kind)
	ref = strings.ToLower(strings.TrimSpace(ref))
	if kind == "internal" && ref == u.Username { // self-leave
		if callerRole == roleOwner {
			return ErrInvalid // owner must transfer or delete instead
		}
		_, err := s.store.RemoveMember(id, "internal", u.Username)
		return err
	}
	if callerRole != roleOwner && callerRole != roleAdmin {
		return ErrForbidden
	}
	_, err := s.store.RemoveMember(id, kind, ref)
	return err
}

// SetMemberRole promotes/demotes an internal member between admin and member (owner or admin).
// External members have no manageable role. The owner is untouchable via this path (use transfer).
func (s *Service) SetMemberRole(u *auth.User, id, kind, ref, role string) (GroupView, error) {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if r := roleOf(g, u.Username); r != roleOwner && r != roleAdmin {
		return GroupView{}, ErrForbidden
	}
	if strings.TrimSpace(kind) != "internal" {
		return GroupView{}, ErrInvalid
	}
	if role != roleAdmin && role != roleMember {
		return GroupView{}, ErrInvalid
	}
	ng, err := s.store.SetMemberRole(id, "internal", strings.ToLower(strings.TrimSpace(ref)), role)
	if err != nil {
		return GroupView{}, err
	}
	return s.toGroupView(ng, u.Username), nil
}

// TransferOwnership hands the group to an existing internal member (owner only). The previous
// owner is demoted to admin.
func (s *Service) TransferOwnership(u *auth.User, id, newOwner string) (GroupView, error) {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return GroupView{}, ErrNotFound
	}
	if roleOf(g, u.Username) != roleOwner {
		return GroupView{}, ErrForbidden
	}
	target := strings.ToLower(strings.TrimSpace(newOwner))
	if target == "" || target == u.Username {
		return GroupView{}, ErrInvalid
	}
	ng, err := s.store.SetOwner(id, target)
	if err != nil {
		return GroupView{}, err
	}
	return s.toGroupView(ng, u.Username), nil
}

// ExpandGroup resolves a group to its member contacts (owner first) for mail/icaly recipient
// expansion. The CALLER themselves is omitted — expansion means "the OTHER members to address", so
// selecting a group never adds you to your own To/attendee list. The caller must be a member.
// Members without a resolvable email are dropped and the result is de-duplicated by email.
func (s *Service) ExpandGroup(u *auth.User, id string) ([]Contact, error) {
	g, ok := s.store.GetGroup(id)
	if !ok {
		return nil, ErrNotFound
	}
	if roleOf(g, u.Username) == "" {
		return nil, ErrForbidden
	}
	out := make([]Contact, 0, len(g.Members)+1)
	if g.Owner != u.Username {
		out = append(out, s.internalContact(g.Owner))
	}
	for _, m := range g.Members {
		if m.Kind == "internal" {
			if m.Ref == u.Username {
				continue // never address yourself
			}
			out = append(out, s.internalContact(m.Ref))
		} else {
			out = append(out, externalMemberContact(m))
		}
	}
	return dedupeByEmail(out), nil
}

// GroupMemberUsernames returns the holistic usernames a personal group grants access to — the owner
// plus every internal member. External (email) members are omitted: they have no account to grant.
// Unscoped (no session): for a trusted service-to-service caller (icaly calendar sharing) that has
// already authenticated with the shared internal secret. Returns ok=false for an unknown group.
func (s *Service) GroupMemberUsernames(id string) (name string, usernames []string, ok bool) {
	g, found := s.store.GetGroup(id)
	if !found {
		return "", nil, false
	}
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			usernames = append(usernames, u)
		}
	}
	add(g.Owner)
	for _, m := range g.Members {
		if m.Kind == "internal" {
			add(m.Ref)
		}
	}
	return g.Name, usernames, true
}

// LookupGroups returns the caller's groups whose name matches q (case-insensitive substring),
// name-sorted and capped at limit (<= 0 means no cap). Backs lookup?includeGroups=1.
func (s *Service) LookupGroups(u *auth.User, q string, limit int) []GroupLookup {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []GroupLookup
	for _, g := range s.store.ListGroupsFor(u.Username) {
		if q != "" && !strings.Contains(strings.ToLower(g.Name), q) {
			continue
		}
		out = append(out, GroupLookup{ID: g.ID, Name: g.Name, MemberCount: len(g.Members) + 1})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// --- helpers ---

// roleOf returns the caller's role in g ("owner"|"admin"|"member"), or "" if not a participant.
func roleOf(g store.Group, username string) string {
	if g.Owner == username {
		return roleOwner
	}
	for _, m := range g.Members {
		if m.Kind == "internal" && m.Ref == username {
			if m.Role == roleAdmin {
				return roleAdmin
			}
			return roleMember
		}
	}
	return ""
}

// canSeeInternal reports whether the acting user may add `other` as an internal member: `other`
// must be a real managed user (not the caller) that the caller can already see under the read-model.
func (s *Service) canSeeInternal(u *auth.User, other string) bool {
	if other == "" || other == u.Username {
		return false
	}
	if !s.isManagedUser(other) {
		return false
	}
	return s.visible(u, directory.ContactGroupsOf(u.Groups), other)
}

// isManagedUser reports whether name is one of the directory's managed accounts.
func (s *Service) isManagedUser(name string) bool {
	for _, m := range s.dir.Members() {
		if m == name {
			return true
		}
	}
	return false
}

// ownExternalEmail returns the display label for the owner's external contact with this email,
// and whether one exists. This both authorizes the add (it must be the caller's own contact) and
// supplies the display snapshot stored on the external member.
func (s *Service) ownExternalEmail(owner, email string) (string, bool) {
	for _, c := range s.store.ListExternal(owner) {
		if strings.ToLower(strings.TrimSpace(c.Email)) == email {
			return externalDisplay(c), true
		}
	}
	return "", false
}

// toGroupView composes the DTO, resolving the owner + every member and tagging the caller's role.
// Non-owner members are role/name sorted; the owner is always first.
func (s *Service) toGroupView(g store.Group, caller string) GroupView {
	members := make([]MemberView, 0, len(g.Members)+1)
	members = append(members, s.ownerView(g.Owner))
	for _, m := range g.Members {
		members = append(members, s.memberView(m))
	}
	sortMembers(members[1:])
	return GroupView{
		ID:          g.ID,
		Name:        g.Name,
		Owner:       g.Owner,
		Role:        roleOf(g, caller),
		Members:     members,
		MemberCount: len(members),
		Created:     g.Created,
		Updated:     g.Updated,
	}
}

// ownerView builds the synthetic owner row (resolved like any internal contact).
func (s *Service) ownerView(owner string) MemberView {
	p := s.prof.Load(owner)
	return MemberView{
		Kind:        "internal",
		Ref:         owner,
		DisplayName: p.DisplayName(),
		Email:       s.inst.Address(owner),
		AvatarURL:   p.AvatarURL,
		Role:        roleOwner,
	}
}

// memberView resolves one stored member into a MemberView.
func (s *Service) memberView(m store.GroupMember) MemberView {
	if m.Kind == "internal" {
		p := s.prof.Load(m.Ref)
		return MemberView{
			Kind:        "internal",
			Ref:         m.Ref,
			DisplayName: p.DisplayName(),
			Email:       s.inst.Address(m.Ref),
			AvatarURL:   p.AvatarURL,
			Role:        m.Role,
		}
	}
	return MemberView{
		Kind:        "external",
		Ref:         m.Ref,
		DisplayName: externalMemberName(m),
		Email:       m.Ref,
		AvatarURL:   externalAvatar(m.Ref),
		Role:        roleMember,
	}
}

// internalContact builds the Contact for an internal member (same shape as Service.internal()).
func (s *Service) internalContact(username string) Contact {
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
		Editable:    false,
	}
}

// externalMemberContact builds the Contact for an external member (email is the identity + id).
func externalMemberContact(m store.GroupMember) Contact {
	return Contact{
		Kind:        "external",
		ID:          m.Ref,
		DisplayName: externalMemberName(m),
		Email:       m.Ref,
		AvatarURL:   externalAvatar(m.Ref),
		Editable:    false,
	}
}

// externalMemberName returns the stored label snapshot, falling back to the email.
func externalMemberName(m store.GroupMember) string {
	if n := strings.TrimSpace(m.Name); n != "" {
		return n
	}
	return m.Ref
}

// dedupeByEmail drops empty-email and duplicate-email contacts (stable, first-wins).
func dedupeByEmail(in []Contact) []Contact {
	seen := make(map[string]bool, len(in))
	out := make([]Contact, 0, len(in))
	for _, c := range in {
		e := strings.ToLower(strings.TrimSpace(c.Email))
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, c)
	}
	return out
}

// sortMembers orders members by role (owner, admin, member) then display name.
func sortMembers(ms []MemberView) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ri, rj := roleRank(ms[i].Role), roleRank(ms[j].Role); ri != rj {
			return ri < rj
		}
		return strings.ToLower(ms[i].DisplayName) < strings.ToLower(ms[j].DisplayName)
	})
}

func roleRank(role string) int {
	switch role {
	case roleOwner:
		return 0
	case roleAdmin:
		return 1
	default:
		return 2
	}
}
