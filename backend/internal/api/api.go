// Package api serves contax's HTTP surface under /api/services/contax/, behind the shared
// holistic session. Every signed-in user manages their OWN contacts (there is no fine-grained
// right to gate — who a user may SEE internally is governed centrally by privleg's contact groups,
// not by a contax permission), so the routes require only a valid session; mutations add the CSRF
// double-submit guard. Error bodies match holistic's contract: {"detail": "..."}.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"contax/internal/auth"
	"contax/internal/contacts"
	"contax/internal/gravatar"
)

const (
	base    = "/api/services/contax/"
	service = "contax"
	version = "0.1.0"
)

// emailRE is a deliberately permissive sanity check — real validation is delivery's job.
var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// groupIDRe matches the ids store.newGroupID mints ("grp-" + 8 hex chars).
var groupIDRe = regexp.MustCompile(`^grp-[0-9a-f]{8}$`)

// Server wires the session verifier and the contacts read-model into HTTP handlers.
type Server struct {
	v              *auth.Verifier
	svc            *contacts.Service
	gravatr        *http.Client
	internalSecret string // shared secret for the machine-to-machine internal/ endpoints ("" disables)
}

// New builds a server. internalSecret guards the service-to-service internal/ endpoints (e.g. icaly
// resolving a personal group's members for calendar sharing); "" disables them (fail closed).
func New(v *auth.Verifier, svc *contacts.Service, internalSecret string) *Server {
	return &Server{v: v, svc: svc, gravatr: &http.Client{Timeout: 6 * time.Second}, internalSecret: internalSecret}
}

type handler func(w http.ResponseWriter, r *http.Request, u *auth.User)

// Handler returns the routed http.Handler (Go 1.22 method+path patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+base+"info", s.guard(false, s.info))
	// The full contact list for the dashboard (internal, computed + external, owned).
	mux.HandleFunc("GET "+base+"contacts", s.guard(false, s.listContacts))
	// Typeahead directory for mail/icaly (cross-service via apiFor('contax')).
	mux.HandleFunc("GET "+base+"lookup", s.guard(false, s.lookup))
	// External-contact CRUD (mutations => CSRF).
	mux.HandleFunc("POST "+base+"contacts", s.guard(true, s.addExternal))
	mux.HandleFunc("PUT "+base+"contacts/{id}", s.guard(true, s.updateExternal))
	mux.HandleFunc("DELETE "+base+"contacts/{id}", s.guard(true, s.deleteExternal))
	mux.HandleFunc("POST "+base+"contacts/{id}/hide", s.guard(true, s.hideExternal(true)))
	mux.HandleFunc("POST "+base+"contacts/{id}/unhide", s.guard(true, s.hideExternal(false)))
	// Internal contacts can only be hidden/unhidden (never deleted or edited).
	mux.HandleFunc("POST "+base+"internal/{username}/hide", s.guard(true, s.hideInternal(true)))
	mux.HandleFunc("POST "+base+"internal/{username}/unhide", s.guard(true, s.hideInternal(false)))
	// Personal ("contax-level") contact groups the caller owns or belongs to. Reads need only a
	// session; mutations add CSRF. Intra-group authority (owner/admin/member) is enforced in the
	// service layer — there is no privleg right for this.
	mux.HandleFunc("GET "+base+"groups", s.guard(false, s.listGroups))
	mux.HandleFunc("POST "+base+"groups", s.guard(true, s.createGroup))
	mux.HandleFunc("GET "+base+"groups/{id}", s.guard(false, s.getGroup))
	mux.HandleFunc("PUT "+base+"groups/{id}", s.guard(true, s.renameGroup))
	mux.HandleFunc("DELETE "+base+"groups/{id}", s.guard(true, s.deleteGroup))
	// Expansion for mail/icaly (reached cross-service via apiFor('contax')): group -> member contacts.
	mux.HandleFunc("GET "+base+"groups/{id}/members", s.guard(false, s.groupMembers))
	mux.HandleFunc("POST "+base+"groups/{id}/members", s.guard(true, s.addGroupMember))
	mux.HandleFunc("PUT "+base+"groups/{id}/members", s.guard(true, s.setGroupMemberRole))
	mux.HandleFunc("DELETE "+base+"groups/{id}/members", s.guard(true, s.removeGroupMember))
	mux.HandleFunc("POST "+base+"groups/{id}/transfer", s.guard(true, s.transferGroup))
	// Server-side Gravatar proxy for external contacts (no third-party origin in the browser).
	mux.HandleFunc("GET "+base+"avatar/ext", s.guard(false, s.avatarExt))
	// Machine-to-machine: resolve a personal group's internal member usernames. Shared-secret auth
	// (never a session) — icaly's calendar sharing calls this to live-resolve contax-group grants.
	mux.HandleFunc("GET "+base+"internal/groups/{id}/members", s.internalGroupMembers)
	mux.HandleFunc("GET "+base+"health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return mux
}

// guard authenticates and optionally enforces CSRF. contax declares no fine-grained rights, so
// there is no per-route permission to check — a valid holistic session is the only requirement.
func (s *Server) guard(csrf bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.v.User(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		if csrf && !s.v.CheckCSRF(r) {
			writeErr(w, http.StatusForbidden, "CSRF check failed")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": service,
		"version": version,
		"user":    u.Username,
		"isAdmin": u.IsAdmin,
	})
}

func (s *Server) listContacts(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	writeJSON(w, http.StatusOK, map[string]any{"contacts": s.svc.List(u)})
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	limit := 8
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 50 {
		limit = n
	}
	q := r.URL.Query().Get("q")
	resp := map[string]any{"contacts": s.svc.Lookup(u, q, limit)}
	// Opt-in so the default typeahead stays byte-compatible for callers that don't want groups.
	if r.URL.Query().Get("includeGroups") == "1" {
		resp["groups"] = s.svc.LookupGroups(u, q, limit)
	}
	writeJSON(w, http.StatusOK, resp)
}

// externalBody is the payload for creating/updating an external contact.
type externalBody struct {
	Nickname  string `json:"nickname"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"email"`
}

func decodeExternal(w http.ResponseWriter, r *http.Request) (externalBody, bool) {
	var b externalBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&b); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return externalBody{}, false
	}
	b.Nickname = strings.TrimSpace(b.Nickname)
	b.FirstName = strings.TrimSpace(b.FirstName)
	b.LastName = strings.TrimSpace(b.LastName)
	b.Email = strings.TrimSpace(b.Email)
	if !emailRE.MatchString(b.Email) {
		writeErr(w, http.StatusBadRequest, "A valid email address is required")
		return externalBody{}, false
	}
	return b, true
}

func (s *Server) addExternal(w http.ResponseWriter, r *http.Request, u *auth.User) {
	b, ok := decodeExternal(w, r)
	if !ok {
		return
	}
	c, err := s.svc.AddExternal(u.Username, b.Nickname, b.FirstName, b.LastName, b.Email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the contact")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) updateExternal(w http.ResponseWriter, r *http.Request, u *auth.User) {
	b, ok := decodeExternal(w, r)
	if !ok {
		return
	}
	c, err := s.svc.UpdateExternal(u.Username, r.PathValue("id"), b.Nickname, b.FirstName, b.LastName, b.Email)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteExternal(w http.ResponseWriter, r *http.Request, u *auth.User) {
	if err := s.svc.DeleteExternal(u.Username, r.PathValue("id")); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) hideExternal(hidden bool) handler {
	return func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		if err := s.svc.SetExternalHidden(u.Username, r.PathValue("id"), hidden); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) hideInternal(hidden bool) handler {
	return func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		name := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
		if name == "" || name == u.Username {
			writeErr(w, http.StatusBadRequest, "Invalid user")
			return
		}
		if err := s.svc.SetInternalHidden(u.Username, name, hidden); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not update visibility")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// --- personal groups ---

// groupID reads and validates the {id} path value, writing a 404 on a malformed id.
func groupID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !groupIDRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "Group not found")
		return "", false
	}
	return id, true
}

// groupNameBody is the payload for create/rename.
type groupNameBody struct {
	Name string `json:"name"`
}

// decodeGroupName decodes + sanitises a group name (trim, collapse whitespace, cap 64 runes).
func decodeGroupName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var b groupNameBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&b); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return "", false
	}
	name := strings.Join(strings.Fields(b.Name), " ")
	if runes := []rune(name); len(runes) > 64 {
		name = strings.TrimSpace(string(runes[:64]))
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest, "A group name is required")
		return "", false
	}
	return name, true
}

// memberBody is the payload for add-member (kind, ref) and set-role (kind, ref, role).
type memberBody struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Role string `json:"role"`
}

func decodeMember(w http.ResponseWriter, r *http.Request) (memberBody, bool) {
	var b memberBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&b); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return memberBody{}, false
	}
	b.Kind = strings.TrimSpace(b.Kind)
	b.Ref = strings.TrimSpace(b.Ref)
	b.Role = strings.TrimSpace(b.Role)
	return b, true
}

func (s *Server) listGroups(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	writeJSON(w, http.StatusOK, map[string]any{"groups": s.svc.ListGroups(u)})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	name, ok := decodeGroupName(w, r)
	if !ok {
		return
	}
	g, err := s.svc.CreateGroup(u, name)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	g, err := s.svc.GetGroup(u, id)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// internalGroupMembers resolves a personal group's internal member usernames (owner + internal
// members) for a trusted service-to-service caller. Authenticated solely by the shared secret
// (constant-time), never a session; "" secret fails closed with 503.
func (s *Server) internalGroupMembers(w http.ResponseWriter, r *http.Request) {
	if s.internalSecret == "" {
		writeErr(w, http.StatusServiceUnavailable, "Internal API not configured")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Contax-Internal-Secret")), []byte(s.internalSecret)) != 1 {
		writeErr(w, http.StatusUnauthorized, "Not authenticated")
		return
	}
	id := r.PathValue("id")
	if !groupIDRe.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "Invalid group id")
		return
	}
	name, usernames, ok := s.svc.GroupMemberUsernames(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Group not found")
		return
	}
	if usernames == nil {
		usernames = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "usernames": usernames})
}

func (s *Server) renameGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	name, ok := decodeGroupName(w, r)
	if !ok {
		return
	}
	g, err := s.svc.RenameGroup(u, id, name)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	if err := s.svc.DeleteGroup(u, id); err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// groupMembers expands a group into its member contacts (mail/icaly recipient expansion).
func (s *Server) groupMembers(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	cs, err := s.svc.ExpandGroup(u, id)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": cs})
}

func (s *Server) addGroupMember(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	b, ok := decodeMember(w, r)
	if !ok {
		return
	}
	g, err := s.svc.AddMember(u, id, b.Kind, b.Ref)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) setGroupMemberRole(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	b, ok := decodeMember(w, r)
	if !ok {
		return
	}
	g, err := s.svc.SetMemberRole(u, id, b.Kind, b.Ref, b.Role)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// removeGroupMember takes kind/ref as query params (a DELETE with no body, so an email ref rides
// the query string), and doubles as "leave" when a member removes themselves.
func (s *Server) removeGroupMember(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if err := s.svc.RemoveMember(u, id, kind, ref); err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) transferGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	var b struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&b); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	g, err := s.svc.TransferOwnership(u, id, b.Username)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// writeGroupErr maps the group-layer sentinels to the holistic {detail} error contract.
func (s *Server) writeGroupErr(w http.ResponseWriter, err error) {
	switch err {
	case contacts.ErrNotFound:
		writeErr(w, http.StatusNotFound, "Group not found")
	case contacts.ErrForbidden:
		writeErr(w, http.StatusForbidden, "Not allowed")
	case contacts.ErrInvalid:
		writeErr(w, http.StatusBadRequest, "Invalid request")
	case contacts.ErrExists:
		writeErr(w, http.StatusConflict, "Already a member")
	default:
		writeErr(w, http.StatusInternalServerError, "Could not complete the request")
	}
}

// avatarExt proxies a Gravatar image for an external contact. The email is only hashed to a fixed
// gravatar.com URL, so there is no SSRF surface. A miss (or any upstream failure) returns 404 and
// the UI falls back to initials.
func (s *Server) avatarExt(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	upstream := gravatar.UpstreamURL(r.URL.Query().Get("email"))
	if upstream == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	resp, err := s.gravatr.Get(upstream)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, http.MaxBytesReader(w, resp.Body, 1<<20))
}

func (s *Server) writeStoreErr(w http.ResponseWriter, err error) {
	if err == contacts.ErrNotFound {
		writeErr(w, http.StatusNotFound, "Contact not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, "Could not update the contact")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
