package mcphttp

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// isLoopbackHostHeader reports whether addr (a bare host or a host:port pair,
// as found in an HTTP Host header) refers to a loopback interface:
// "localhost", "127.0.0.1", "::1", and their host:port and bracketed forms.
// Mirrors mcp-go's own isLoopbackHost (internal/unexported to that module),
// reimplemented here so AllowedHosts always permits loopback regardless of
// the configured allowlist.
func isLoopbackHostHeader(addr string) bool {
	host := hostWithoutPort(addr)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return ip.IsLoopback()
}

// hostWithoutPort strips an optional ":port" suffix from a Host header value
// for allowlist comparison. Bracketed IPv6 literals ("[::1]:3000") have their
// brackets stripped too, matching net.SplitHostPort's behavior.
func hostWithoutPort(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// addr had no port to split (net.SplitHostPort errors on a bare host).
		return strings.Trim(addr, "[]")
	}
	return host
}

// AllowedHosts wraps next with an explicit Host-header allowlist. It replaces
// mcp-go's built-in DNS-rebinding protection (see NewMCPHandler and
// NewSSEHandler), which rejects any non-loopback Host whenever the server's
// own listening address is loopback. That is exactly the openbrain-web
// deployment shape (OPENBRAIN_WEB_HOST=127.0.0.1 behind cloudflared):
// cloudflared forwards "Host: openbrain.wr-s.net" unchanged, so the built-in
// check 403'd every remote MCP request (OB-054).
//
// This allowlist keeps the same DNS-rebinding intent, reject a Host the
// operator did not name, while explicitly permitting the public host(s) the
// service is actually meant to serve. Loopback hosts (localhost, 127.0.0.1,
// ::1, in bare or host:port form) are always permitted in addition to the
// configured allowlist, so local tooling and health checks keep working
// regardless of configuration.
func AllowedHosts(allowed []string, next http.Handler) http.Handler {
	allowSet := make(map[string]struct{}, len(allowed))
	for _, h := range allowed {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		allowSet[h] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackHostHeader(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := allowSet[strings.ToLower(hostWithoutPort(r.Host))]; ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q", r.Host), http.StatusForbidden)
	})
}
