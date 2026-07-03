// Package gravatar builds the Gravatar image URL for an external contact's email. An external
// contact's picture may only be the one their mail provider exposes (never an upload); Gravatar
// is the provider-agnostic de-facto source. contaxd proxies the image server-side (so the browser
// never talks to a third party and no external origin is needed in the CSP), and asks Gravatar to
// 404 rather than serve a generic placeholder when the address is unknown — the UI then falls back
// to initials.
package gravatar

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
)

// Hash is the Gravatar hash of an email: md5 of the lowercased, trimmed address.
func Hash(email string) string {
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// UpstreamURL is the Gravatar URL contaxd fetches. d=404 => 404 when the address has no avatar
// (so the caller can fall back to initials); s=200 => a reasonable icon size.
func UpstreamURL(email string) string {
	if strings.TrimSpace(email) == "" {
		return ""
	}
	return "https://www.gravatar.com/avatar/" + Hash(email) + "?d=404&s=200"
}
