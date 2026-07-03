// Package api serves contax's HTTP surface under /api/services/contax/, behind the shared
// holistic session. Every signed-in user manages their OWN contacts (there is no fine-grained
// right to gate — who a user may SEE internally is governed centrally by privleg's contact groups,
// not by a contax permission), so the routes require only a valid session; mutations add the CSRF
// double-submit guard. Error bodies match holistic's contract: {"detail": "..."}.
package api

import (
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

// Server wires the session verifier and the contacts read-model into HTTP handlers.
type Server struct {
	v       *auth.Verifier
	svc     *contacts.Service
	gravatr *http.Client
}

// New builds a server.
func New(v *auth.Verifier, svc *contacts.Service) *Server {
	return &Server{v: v, svc: svc, gravatr: &http.Client{Timeout: 6 * time.Second}}
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
	writeJSON(w, http.StatusOK, map[string]any{"contacts": s.svc.Lookup(u, r.URL.Query().Get("q"), limit)})
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
