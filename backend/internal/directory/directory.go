// Package directory enumerates holistic-managed users (members of the smbusers group, the same
// set privleg and the dashboard admin API list) and resolves each user's Linux groups live from
// the OS. Contact visibility is computed from the "hc_*" groups two users share — those groups
// are materialised by privleg from a group whose ContactVisibility flag is on. contax never asks
// privleg anything at request time: Linux groups are the single source of truth and contax reads
// them directly, so privleg stays out of the request path (parity with how every service enforces
// hp_* rights). Group lookups are cached briefly since a list request resolves every user.
package directory

import (
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// ContactGroupPrefix marks a Linux group as a contact-visibility group. privleg materialises one
// such group per contact-enabled group definition; membership means "participates in that web".
const ContactGroupPrefix = "hc_"

// Directory enumerates managed users and resolves their groups.
type Directory struct {
	enumGroup string

	mu    sync.Mutex
	cache map[string]groupEntry
}

type groupEntry struct {
	groups []string
	at     time.Time
}

// New builds a directory. enumGroup defaults to "smbusers".
func New(enumGroup string) *Directory {
	if enumGroup == "" {
		enumGroup = "smbusers"
	}
	return &Directory{enumGroup: enumGroup, cache: make(map[string]groupEntry)}
}

// Members returns the sorted usernames of holistic-managed accounts (empty if the group is
// missing — a host with no managed users).
func (d *Directory) Members() []string {
	out, err := exec.Command("getent", "group", d.enumGroup).Output()
	if err != nil {
		return nil
	}
	// getent group line: name:passwd:gid:member1,member2,...
	fields := strings.SplitN(strings.TrimSpace(string(out)), ":", 4)
	if len(fields) < 4 || fields[3] == "" {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, m := range strings.Split(fields[3], ",") {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			seen[m] = true
			names = append(names, m)
		}
	}
	sort.Strings(names)
	return names
}

// Groups returns a user's Linux groups, read live from the OS and cached for 30s.
func (d *Directory) Groups(username string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.cache[username]; ok && time.Since(e.at) < 30*time.Second {
		return e.groups
	}
	var groups []string
	if out, err := exec.Command("id", "-nG", username).Output(); err == nil {
		groups = strings.Fields(string(out))
	}
	d.cache[username] = groupEntry{groups: groups, at: time.Now()}
	return groups
}

// ContactGroups returns the set of a user's contact-visibility groups (the hc_* subset).
func (d *Directory) ContactGroups(username string) map[string]bool {
	m := map[string]bool{}
	for _, g := range d.Groups(username) {
		if strings.HasPrefix(g, ContactGroupPrefix) {
			m[g] = true
		}
	}
	return m
}

// ContactGroupsOf returns the hc_* subset of an already-resolved group list (used for the caller,
// whose groups the session verifier resolved once).
func ContactGroupsOf(groups []string) map[string]bool {
	m := map[string]bool{}
	for _, g := range groups {
		if strings.HasPrefix(g, ContactGroupPrefix) {
			m[g] = true
		}
	}
	return m
}
