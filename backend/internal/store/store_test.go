package store

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "contacts.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
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
