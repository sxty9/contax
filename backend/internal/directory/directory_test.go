package directory

import (
	"testing"
	"time"
)

// seed pre-fills the group cache for a user with a fresh timestamp, so Groups/ContactGroups serve
// it without shelling out to the OS — the tests stay deterministic and independent of real accounts.
func seed(d *Directory, username string, groups ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache[username] = groupEntry{groups: groups, at: time.Now()}
}

// ── atomic access (Atomare Zugriffe) ─────────────────────────────────────────────────────

// The cache is a shared pool: Groups must never hand out a reference to the slice it stores, or a
// caller mutating the return value would change what the next reader observes — an observable
// intermediate state. Mutating the returned slice must leave the cache untouched.
func TestGroupsReturnCopyIsolatesCache(t *testing.T) {
	d := New("")
	seed(d, "ada", "hc_team", "sudo")

	got := d.Groups("ada")
	if len(got) != 2 {
		t.Fatalf("Groups = %v, want 2 entries", got)
	}
	got[0] = "MUTATED"
	got = append(got, "injected")

	again := d.Groups("ada")
	if again[0] != "hc_team" || len(again) != 2 {
		t.Fatalf("caller mutation leaked into the cache: %v", again)
	}
}

// ContactGroups projects only the hc_* subset (the contact-visibility groups), and builds a fresh
// map — the passive cache is never interpreted in place.
func TestContactGroupsFiltersPrefix(t *testing.T) {
	d := New("")
	seed(d, "ada", "hc_team", "sudo", "hc_family", "smbusers")

	cg := d.ContactGroups("ada")
	if len(cg) != 2 || !cg["hc_team"] || !cg["hc_family"] {
		t.Fatalf("ContactGroups = %v, want {hc_team, hc_family}", cg)
	}
	if cg["sudo"] || cg["smbusers"] {
		t.Fatalf("ContactGroups leaked a non-hc_ group: %v", cg)
	}
}

// ContactGroupsOf is the same hc_* projection over an already-resolved list (the caller's own groups
// from the session), with no cache involved.
func TestContactGroupsOfFiltersPrefix(t *testing.T) {
	cg := ContactGroupsOf([]string{"hc_a", "wheel", "hc_b"})
	if len(cg) != 2 || !cg["hc_a"] || !cg["hc_b"] {
		t.Fatalf("ContactGroupsOf = %v, want {hc_a, hc_b}", cg)
	}
}
