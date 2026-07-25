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

	// internalSecretHeader carries the shared machine-to-machine secret on the internal/* endpoints
	// (no browser session). A sibling service (e.g. hosuto) presents it to resolve group membership.
	internalSecretHeader = "X-Contax-Internal-Secret"
)

// emailRE is a deliberately permissive sanity check — real validation is delivery's job.
var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Server wires the session verifier and the contacts read-model into HTTP handlers.
type Server struct {
	v       *auth.Verifier
	svc     *contacts.Service
	gravatr *http.Client
	// internal is the shared secret guarding the machine-to-machine internal/* endpoints. "" leaves
	// them disabled (fail closed) — a host that never provisioned the secret simply serves 503 there.
	internal string
}

// New builds a server. internalSecret guards the M2M internal/* endpoints; "" disables them.
func New(v *auth.Verifier, svc *contacts.Service, internalSecret string) *Server {
	return &Server{v: v, svc: svc, gravatr: &http.Client{Timeout: 6 * time.Second}, internal: internalSecret}
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
	// Personal contact groups — the entity contax owns for the shared ContactPicker and sibling
	// services. Session-gated CRUD; mutations add CSRF. GET members backs ContactPicker.onExpandGroup.
	mux.HandleFunc("GET "+base+"groups", s.guard(false, s.listGroups))
	mux.HandleFunc("POST "+base+"groups", s.guard(true, s.createGroup))
	mux.HandleFunc("PUT "+base+"groups/{id}", s.guard(true, s.renameGroup))
	mux.HandleFunc("DELETE "+base+"groups/{id}", s.guard(true, s.deleteGroup))
	mux.HandleFunc("GET "+base+"groups/{id}/members", s.guard(false, s.groupMembers))
	mux.HandleFunc("POST "+base+"groups/{id}/members", s.guard(true, s.addGroupMember))
	mux.HandleFunc("DELETE "+base+"groups/{id}/members/{ref}", s.guard(true, s.removeGroupMember))
	// Machine-to-machine: resolve a group's internal member usernames by id. Shared-secret auth, no
	// session — a sibling service keeps a "shared with this group" membership live through it.
	mux.HandleFunc("GET "+base+"internal/groups/{id}/members", s.internalGroupMembers)
	// Server-side Gravatar proxy for external contacts (no third-party origin in the browser).
	mux.HandleFunc("GET "+base+"avatar/ext", s.guard(false, s.avatarExt))
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
	// The shared ContactPicker asks for groups with includeGroups=1; a picked group is expanded via
	// GET groups/{id}/members. Hosts that don't opt in never see (and never need) the groups key.
	if v := r.URL.Query().Get("includeGroups"); v == "1" || v == "true" {
		resp["groups"] = s.svc.LookupGroups(u.Username, q)
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

// --- personal contact groups ---

type groupBody struct {
	Name string `json:"name"`
}

// memberBody is what the ContactPicker yields for one member: an internal user carries a username;
// an external contact carries only its address.
type memberBody struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

func (s *Server) listGroups(w http.ResponseWriter, _ *http.Request, u *auth.User) {
	writeJSON(w, http.StatusOK, map[string]any{"groups": s.svc.Groups(u.Username)})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	name, ok := decodeGroupName(w, r)
	if !ok {
		return
	}
	g, err := s.svc.CreateGroup(u.Username, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not create the group")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) renameGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	name, ok := decodeGroupName(w, r)
	if !ok {
		return
	}
	g, err := s.svc.RenameGroup(u.Username, r.PathValue("id"), name)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request, u *auth.User) {
	if err := s.svc.DeleteGroup(u.Username, r.PathValue("id")); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// groupMembers resolves a group to its member contacts — the session-side endpoint the shared
// ContactPicker wires to onExpandGroup, and the same one contax's own group editor reads.
func (s *Server) groupMembers(w http.ResponseWriter, r *http.Request, u *auth.User) {
	members, ok := s.svc.GroupMembers(u.Username, r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Group not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": members})
}

func (s *Server) addGroupMember(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var b memberBody
	if !decodeJSON(w, r, &b) {
		return
	}
	g, err := s.svc.AddGroupMember(u, r.PathValue("id"), b.Username, b.Email)
	if err != nil {
		if err == contacts.ErrNotAddressable {
			writeErr(w, http.StatusBadRequest, "That contact cannot be added to a group")
			return
		}
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) removeGroupMember(w http.ResponseWriter, r *http.Request, u *auth.User) {
	g, err := s.svc.RemoveGroupMember(u.Username, r.PathValue("id"), r.PathValue("ref"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// internalGroupMembers is the machine-to-machine endpoint: it resolves a group's internal member
// usernames from the group id alone, authenticated solely by the shared secret (constant-time),
// never a session. "" secret => disabled (503), so a misconfigured deploy fails closed.
func (s *Server) internalGroupMembers(w http.ResponseWriter, r *http.Request) {
	if s.internal == "" {
		writeErr(w, http.StatusServiceUnavailable, "Internal API not configured")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(internalSecretHeader)), []byte(s.internal)) != 1 {
		writeErr(w, http.StatusUnauthorized, "Not authenticated")
		return
	}
	usernames, ok := s.svc.InternalGroupMembers(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Group not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"usernames": usernames})
}

// decodeGroupName reads and validates a {name} body for group create/rename.
func decodeGroupName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var b groupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&b); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return "", false
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "A group name is required")
		return "", false
	}
	if len([]rune(name)) > 120 {
		writeErr(w, http.StatusBadRequest, "Group name is too long")
		return "", false
	}
	return name, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(v); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return false
	}
	return true
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
