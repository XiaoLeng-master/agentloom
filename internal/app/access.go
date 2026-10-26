package app

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// AccessPolicy separates public read/use access from configuration-changing
// administration. Local mode retains the zero-configuration demo token.
type AccessPolicy struct {
	PublicMode  bool
	PublicToken string
	AdminToken  string
}

func localAccessPolicy() AccessPolicy {
	return AccessPolicy{PublicMode: true, AdminToken: "local-demo"}
}

func (p AccessPolicy) publicAllowed(r *http.Request) bool {
	if p.PublicMode && p.PublicToken == "" {
		return true
	}
	return tokenMatches(r, p.PublicToken)
}

func (p AccessPolicy) adminAllowed(r *http.Request) bool {
	return p.AdminToken != "" && tokenMatches(r, p.AdminToken)
}

func tokenMatches(r *http.Request, expected string) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if expected == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func requestTenant(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
