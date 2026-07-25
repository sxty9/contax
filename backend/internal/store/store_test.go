package store

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data", "contacts.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func add(t *testing.T, s *Store, owner, email string) ExternalContact {
	t.Helper()
	c, err := s.AddExternal(owner, ExternalContact{Email: email})
	if err != nil {
		t.Fatalf("AddExternal: %v", err)
	}
	return c
}

// A save must round-trip through disk with no loss: every write path is exercised, the store is
// reopened from the same file, and the reloaded state must equal what was written. This is the
// end-to-end proof that the atomic temp→fsync→rename actually persists what it claims to.
func TestPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "contacts.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := add(t, s, "ada", "a@x.test")
	b := add(t, s, "ada", "b@x.test")
	if _, err := s.UpdateExternal("ada", a.ID, "nick", "Ada", "Lovelace", "ada@x.test"); err != nil {
		t.Fatalf("UpdateExternal: %v", err)
	}
	if err := s.SetExternalHidden("ada", b.ID, true); err != nil {
		t.Fatalf("SetExternalHidden: %v", err)
	}
	if err := s.SetInternalHidden("ada", "grace", true); err != nil {
		t.Fatalf("SetInternalHidden: %v", err)
	}

	// Reopen from disk — a fresh Store that only knows what save() persisted.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ext := s2.ListExternal("ada")
	if len(ext) != 2 {
		t.Fatalf("reloaded %d external contacts, want 2", len(ext))
	}
	byID := map[string]ExternalContact{}
	for _, c := range ext {
		byID[c.ID] = c
	}
	if got := byID[a.ID]; got.FirstName != "Ada" || got.LastName != "Lovelace" || got.Email != "ada@x.test" {
		t.Fatalf("update did not survive reload: %+v", got)
	}
	if got := byID[b.ID]; !got.Hidden {
		t.Fatalf("hidden flag did not survive reload: %+v", got)
	}
	if !s2.HiddenInternal("ada")["grace"] {
		t.Fatal("hidden-internal set did not survive reload")
	}
}

// ── atomic access (Atomare Zugriffe) ─────────────────────────────────────────────────────

// Every write is one critical section, so a failed persist must leave NO observable change: the
// store rolls the in-memory snapshot back to exactly what it was. Here save() is forced to fail
// deterministically (its parent directory is replaced by a regular file, so MkdirAll fails for any
// user, root included), and the add must both error and leave the prior state untouched.
func TestFailedSaveRollsBack(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	path := filepath.Join(dir, "contacts.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	keep := add(t, s, "ada", "keep@x.test")

	// Break the write path: turn the data directory into a regular file so save()'s MkdirAll fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.AddExternal("ada", ExternalContact{Email: "lost@x.test"}); err == nil {
		t.Fatal("AddExternal succeeded despite a broken write path")
	}
	got := s.ListExternal("ada")
	if len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("failed save left an observable intermediate state: %+v", got)
	}
}

// A failed mutation of an existing contact must likewise not be observable: the edited fields must
// not appear in memory once the persist is rejected.
func TestFailedUpdateRollsBack(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	s, err := Open(filepath.Join(dir, "contacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := add(t, s, "ada", "orig@x.test")

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpdateExternal("ada", c.ID, "", "Edited", "", "edited@x.test"); err == nil {
		t.Fatal("UpdateExternal succeeded despite a broken write path")
	}
	got := s.ListExternal("ada")
	if len(got) != 1 || got[0].FirstName != "" || got[0].Email != "orig@x.test" {
		t.Fatalf("failed update left an observable intermediate state: %+v", got)
	}
}

// Many writers hitting one owner concurrently must all land: the per-store mutex makes each
// read-modify-write atomic, so no add clobbers another. Run under -race this also pins the absence
// of a data race on the shared snapshot.
func TestConcurrentAddsAllLand(t *testing.T) {
	s := open(t)
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.AddExternal("ada", ExternalContact{Email: "u" + strconv.Itoa(i) + "@x.test"}); err != nil {
				t.Errorf("AddExternal: %v", err)
			}
		}(i)
	}
	wg.Wait()

	got := s.ListExternal("ada")
	if len(got) != n {
		t.Fatalf("concurrent adds lost updates: got %d, want %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.ID] {
			t.Fatalf("duplicate id %q from concurrent adds", c.ID)
		}
		seen[c.ID] = true
	}
}

// The atomic write must not leave its temp file behind: after a successful save the data directory
// holds exactly the state file, so no partial artifact is ever observable to a reader.
func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(filepath.Join(dir, "contacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	add(t, s, "ada", "a@x.test")
	add(t, s, "ada", "b@x.test")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "contacts.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("data dir holds %v, want exactly [contacts.json]", names)
	}
}

func TestGroupsCRUDAndMembers(t *testing.T) {
	s := open(t)

	g, err := s.CreateGroup("alice", "Team")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.ID == "" || g.Name != "Team" {
		t.Fatalf("unexpected group: %+v", g)
	}
	if got := g.ID[:4]; got != "grp-" {
		t.Fatalf("group id should be prefixed grp-, got %q", g.ID)
	}

	// Add two members; adding the same one again is idempotent.
	if _, err := s.AddGroupMember("alice", g.ID, GroupMember{Kind: "internal", Ref: "bob"}); err != nil {
		t.Fatalf("add internal: %v", err)
	}
	if _, err := s.AddGroupMember("alice", g.ID, GroupMember{Kind: "external", Ref: "ext1"}); err != nil {
		t.Fatalf("add external: %v", err)
	}
	after, err := s.AddGroupMember("alice", g.ID, GroupMember{Kind: "internal", Ref: "bob"})
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if len(after.Members) != 2 {
		t.Fatalf("idempotent add expected 2 members, got %d", len(after.Members))
	}

	// Rename.
	if _, err := s.RenameGroup("alice", g.ID, "Squad"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Remove one member.
	rem, err := s.RemoveGroupMember("alice", g.ID, "bob")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(rem.Members) != 1 || rem.Members[0].Ref != "ext1" {
		t.Fatalf("after remove expected only ext1, got %+v", rem.Members)
	}

	// FindGroup resolves the owner from a bare id (the M2M entry point).
	owner, fg, ok := s.FindGroup(g.ID)
	if !ok || owner != "alice" || fg.Name != "Squad" {
		t.Fatalf("FindGroup = %q %+v %v", owner, fg, ok)
	}

	// Persistence: reopen and the group survives with its members.
	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	gs := s2.ListGroups("alice")
	if len(gs) != 1 || gs[0].Name != "Squad" || len(gs[0].Members) != 1 {
		t.Fatalf("reload mismatch: %+v", gs)
	}
}

func TestGroupsNotFoundAndIsolation(t *testing.T) {
	s := open(t)
	if _, err := s.RenameGroup("alice", "grp-missing", "x"); err != ErrNotFound {
		t.Fatalf("rename missing = %v, want ErrNotFound", err)
	}
	if err := s.DeleteGroup("alice", "grp-missing"); err != ErrNotFound {
		t.Fatalf("delete missing = %v, want ErrNotFound", err)
	}
	if _, err := s.AddGroupMember("alice", "grp-missing", GroupMember{Kind: "internal", Ref: "bob"}); err != ErrNotFound {
		t.Fatalf("add to missing = %v, want ErrNotFound", err)
	}

	// A returned copy must not alias internal state.
	g, _ := s.CreateGroup("alice", "G")
	_, _ = s.AddGroupMember("alice", g.ID, GroupMember{Kind: "internal", Ref: "bob"})
	got := s.ListGroups("alice")
	got[0].Members[0].Ref = "mutated"
	again := s.ListGroups("alice")
	if again[0].Members[0].Ref != "bob" {
		t.Fatalf("ListGroups aliased internal state: %+v", again[0].Members)
	}

	// Groups are per-owner.
	if len(s.ListGroups("carol")) != 0 {
		t.Fatalf("carol should have no groups")
	}
}
