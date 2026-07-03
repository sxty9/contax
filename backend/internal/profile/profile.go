// Package profile reads a holistic account's app-managed profile (first/last name, nickname)
// and avatar metadata from the dashboard's profile store — the single source of truth for the
// extra identity fields the OS has no home for. contaxd runs in group `holistic`, so it can read
// /var/lib/holistic/profiles/<user>.json and stat <user>.avatar, the same way maild's profile
// reader does. Read-only; contax never writes a profile. An internal contact's attributes come
// entirely from here (nickname/first/last) plus the avatar endpoint the dashboard already serves.
package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// nameRE guards against path traversal: a username must look like a Linux account name.
var nameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Profile mirrors the app-managed fields the dashboard persists per user, plus the resolved
// avatar URL (root-relative, cache-busted) when the user has uploaded a photo.
type Profile struct {
	Username  string `json:"username"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatarUrl"`
}

// DisplayName returns "First Last", falling back to the nickname, then the username.
func (p Profile) DisplayName() string {
	if full := strings.TrimSpace(p.FirstName + " " + p.LastName); full != "" {
		return full
	}
	if n := strings.TrimSpace(p.Nickname); n != "" {
		return n
	}
	return p.Username
}

// Resolver reads (and briefly caches) profiles from the dashboard's store.
type Resolver struct {
	root string

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	p  Profile
	at time.Time
}

// New honours HOLISTIC_PROFILES (default /var/lib/holistic/profiles).
func New() *Resolver {
	root := strings.TrimSpace(os.Getenv("HOLISTIC_PROFILES"))
	if root == "" {
		root = "/var/lib/holistic/profiles"
	}
	return &Resolver{root: root, cache: make(map[string]entry)}
}

// Load returns the profile for a user, or a zero profile (username only) if absent/unreadable
// — degrading gracefully exactly like the dashboard does. Cached for 30s per user.
func (r *Resolver) Load(username string) Profile {
	username = strings.ToLower(strings.TrimSpace(username))
	if !nameRE.MatchString(username) {
		return Profile{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.cache[username]; ok && time.Since(e.at) < 30*time.Second {
		return e.p
	}
	p := Profile{Username: username}
	if b, err := os.ReadFile(filepath.Join(r.root, username+".json")); err == nil {
		var raw struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
			Nickname  string `json:"nickname"`
		}
		if json.Unmarshal(b, &raw) == nil {
			p.FirstName = strings.TrimSpace(raw.FirstName)
			p.LastName = strings.TrimSpace(raw.LastName)
			p.Nickname = strings.TrimSpace(raw.Nickname)
		}
	}
	p.AvatarURL = r.avatarURL(username)
	r.cache[username] = entry{p: p, at: time.Now()}
	return p
}

// avatarURL mirrors the dashboard's own avatar_url(): the root-relative, cache-busted URL the
// dashboard serves the photo at, or "" when the user has no avatar. The version is the avatar
// file's mtime, so the URL changes whenever the photo does. This is the SAME path the shell puts
// on HolisticUser.avatarUrl, so the SDK Avatar renders it identically.
func (r *Resolver) avatarURL(username string) string {
	fi, err := os.Stat(filepath.Join(r.root, username+".avatar"))
	if err != nil {
		return ""
	}
	return "/api/account/avatar/" + username + "?v=" + strconv.FormatInt(fi.ModTime().Unix(), 10)
}
