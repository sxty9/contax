// Package instance resolves the canonical mail domain for this holistic instance. The domain
// is owned by the dashboard (single source of truth) and persisted to
// /var/lib/holistic/instance.json; contaxd only READS it. An internal contact's address is
// username@<mailDomain>, exactly like maild/icalyd build it.
package instance

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// Resolver reads the mail domain from the env override or the dashboard's instance.json.
type Resolver struct {
	path     string
	override string

	mu       sync.Mutex
	cached   string
	cachedAt time.Time
}

// New honours HOLISTIC_MAIL_DOMAIN (override) and HOLISTIC_INSTANCE (path to instance.json,
// default /var/lib/holistic/instance.json).
func New() *Resolver {
	path := os.Getenv("HOLISTIC_INSTANCE")
	if path == "" {
		path = "/var/lib/holistic/instance.json"
	}
	return &Resolver{path: path, override: strings.TrimSpace(os.Getenv("HOLISTIC_MAIL_DOMAIN"))}
}

// MailDomain returns the canonical mail domain, or "" if none is known yet. Cached briefly.
func (r *Resolver) MailDomain() string {
	if r.override != "" {
		return r.override
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.cachedAt) < 30*time.Second && r.cached != "" {
		return r.cached
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return ""
	}
	var data struct {
		MailDomain string `json:"mail_domain"`
	}
	if json.Unmarshal(b, &data) != nil {
		return ""
	}
	r.cached = strings.TrimSpace(data.MailDomain)
	r.cachedAt = time.Now()
	return r.cached
}

// Address returns username@<mailDomain>, or just the username if no domain is known yet.
func (r *Resolver) Address(user string) string {
	d := r.MailDomain()
	if d == "" {
		return user
	}
	return user + "@" + d
}
