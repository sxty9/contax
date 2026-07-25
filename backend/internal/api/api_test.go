package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"contax/internal/auth"
	"contax/internal/contacts"
	"contax/internal/directory"
	"contax/internal/instance"
	"contax/internal/profile"
	"contax/internal/store"
)

// newTestServer builds a Server backed by a real (temp) store and empty OS sources. The session
// verifier is never exercised: session handlers are called directly with a synthesized user, and
// the M2M endpoint authenticates by shared secret alone.
func newTestServer(t *testing.T, secret string) (*Server, *store.Store) {
	t.Helper()
	t.Setenv("HOLISTIC_MAIL_DOMAIN", "example.test")
	t.Setenv("HOLISTIC_PROFILES", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "contacts.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := contacts.New(directory.New(""), profile.New(), instance.New(), st)
	return New(auth.NewVerifier([]byte("k"), "sudo"), svc, secret), st
}

// TestInternalGroupMembers covers the machine-to-machine contract hosuto depends on: shared-secret
// auth, owner-plus-internal usernames, external members excluded.
func TestInternalGroupMembers(t *testing.T) {
	srv, st := newTestServer(t, "sekret")
	g, _ := st.CreateGroup("alice", "Team")
	_, _ = st.AddGroupMember("alice", g.ID, store.GroupMember{Kind: "internal", Ref: "bob"})
	_, _ = st.AddGroupMember("alice", g.ID, store.GroupMember{Kind: "external", Ref: "ext1"})
	h := srv.Handler()

	get := func(secret, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, base+"internal/groups/"+id+"/members", nil)
		if secret != "" {
			req.Header.Set(internalSecretHeader, secret)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Correct secret: owner + internal member, sorted; the external member is not a username.
	rec := get("sekret", g.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("ok call: code %d body %s", rec.Code, rec.Body)
	}
	var body struct {
		Usernames []string `json:"usernames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Join(body.Usernames, ",") != "alice,bob" {
		t.Fatalf("usernames = %v, want [alice bob]", body.Usernames)
	}

	if code := get("wrong", g.ID).Code; code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: code %d, want 401", code)
	}
	if code := get("sekret", "grp-nope").Code; code != http.StatusNotFound {
		t.Fatalf("unknown group: code %d, want 404", code)
	}

	// A server with no secret disables the endpoint (fail closed).
	off, _ := newTestServer(t, "")
	req := httptest.NewRequest(http.MethodGet, base+"internal/groups/"+g.ID+"/members", nil)
	req.Header.Set(internalSecretHeader, "anything")
	rec = httptest.NewRecorder()
	off.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled: code %d, want 503", rec.Code)
	}
}

// TestGroupCRUDAndLookup covers the session-side surface: create, add an external member (from what
// the ContactPicker yields), reject an unaddressable internal ref, list members, and surface the
// group through lookup?includeGroups=1.
func TestGroupCRUDAndLookup(t *testing.T) {
	srv, st := newTestServer(t, "")
	u := &auth.User{Username: "alice"}
	_, _ = st.AddExternal("alice", store.ExternalContact{FirstName: "Ext", Email: "ext@example.test"})

	// Create.
	rec := call(srv.createGroup, u, http.MethodPost, base+"groups", `{"name":"Team"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var g contacts.GroupSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &g)
	if g.ID == "" || g.Name != "Team" {
		t.Fatalf("created group: %+v", g)
	}

	// Add an external member by address (no username) — resolves to the owner's own contact.
	rec = call(srv.addGroupMember, u, http.MethodPost, base+"groups/"+g.ID+"/members", `{"email":"ext@example.test"}`, map[string]string{"id": g.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("add external: %d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &g)
	if g.MemberCount != 1 {
		t.Fatalf("memberCount = %d, want 1", g.MemberCount)
	}

	// An internal username nobody here can see is rejected, not silently added.
	rec = call(srv.addGroupMember, u, http.MethodPost, base+"groups/"+g.ID+"/members", `{"username":"bob"}`, map[string]string{"id": g.ID})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("add unaddressable: %d, want 400", rec.Code)
	}

	// Members resolve to full contacts.
	rec = call(srv.groupMembers, u, http.MethodGet, base+"groups/"+g.ID+"/members", "", map[string]string{"id": g.ID})
	var mem struct {
		Contacts []contacts.Contact `json:"contacts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &mem)
	if len(mem.Contacts) != 1 || mem.Contacts[0].Email != "ext@example.test" {
		t.Fatalf("members = %+v", mem.Contacts)
	}

	// lookup?includeGroups=1 surfaces the group; without it there is no groups key.
	rec = call(srv.lookup, u, http.MethodGet, base+"lookup?q=Tea&includeGroups=1", "", nil)
	if !strings.Contains(rec.Body.String(), `"groups"`) || !strings.Contains(rec.Body.String(), "Team") {
		t.Fatalf("lookup includeGroups missing group: %s", rec.Body)
	}
	rec = call(srv.lookup, u, http.MethodGet, base+"lookup?q=Tea", "", nil)
	if strings.Contains(rec.Body.String(), `"groups"`) {
		t.Fatalf("lookup without includeGroups should omit groups: %s", rec.Body)
	}
}

// call invokes a session handler directly with a synthesized user, setting any path values.
func call(h func(http.ResponseWriter, *http.Request, *auth.User), u *auth.User, method, target, body string, pathVals map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, r, u)
	return rec
}
