// Package mcphttp provides HTTP-based MCP transport handlers with
// bearer token authentication for the OpenBrain web server.
package mcphttp

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/embeddings"
	"github.com/windingriverholdings/openbrain/internal/mcptools"
	"github.com/mark3labs/mcp-go/server"
)

// BearerAuth wraps an http.Handler with bearer token authentication.
// When token is non-empty, requests must carry a valid
// "Authorization: Bearer <token>" header; requests without one receive a
// 401 Unauthorized response. When token is empty the handler is passed
// through unchanged and the transport runs open. An empty token is a
// deliberate, operator-selected open mode that mirrors staticAuth on the web
// surface; the caller emits a loud startup warning before mounting an open
// transport.
func BearerAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(auth, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// newMCPServer creates a configured MCP server for HTTP transport.
// The ingest_document tool is excluded because it reads the local filesystem
// and must not be exposed over the network.
// Panics if b is nil — the HTTP transport requires a live Brain.
func newMCPServer(name, version string, b *brain.Brain, embedder embeddings.Embedder) *server.MCPServer {
	if b == nil {
		panic("mcphttp.newMCPServer: brain must not be nil for HTTP transport")
	}
	s := server.NewMCPServer(name, version)
	mcptools.RegisterToolsWithOpts(s, b, embedder, mcptools.RegisterOpts{
		ExcludeIngest: true,
	})
	return s
}

// mcpRequestsPerSecond is the per-IP rate limit for authenticated MCP requests.
const mcpRequestsPerSecond = 1.0 // 60 per minute

// mcpBurstSize is the maximum burst for the MCP rate limiter.
const mcpBurstSize = 10

// NewMCPHandler returns an http.Handler for the Streamable HTTP MCP transport,
// wrapped with rate limiting and bearer token authentication. Mount at "/mcp".
//
// allowedHosts configures the Host-header allowlist (see AllowedHosts) that
// replaces mcp-go's built-in loopback-only DNS-rebinding protection: the
// built-in check 403s every remote request once the server binds a loopback
// address (OB-054), because it has no notion of a legitimate public Host.
// mcp-go's own protection is disabled here in favor of that allowlist, not
// removed outright: DNS-rebinding protection stays enforced, just against an
// explicit list of names instead of a loopback-only heuristic.
func NewMCPHandler(token, name, version string, b *brain.Brain, embedder embeddings.Embedder, allowedHosts []string) http.Handler {
	mcpSrv := newMCPServer(name, version, b, embedder)
	transport := server.NewStreamableHTTPServer(mcpSrv,
		server.WithDisableLocalhostProtection(true),
	)
	return AllowedHosts(allowedHosts, SecureHeaders(RateLimit(mcpRequestsPerSecond, mcpBurstSize, BearerAuth(token, transport))))
}

// NewSSEHandler returns an http.Handler for the SSE MCP transport,
// wrapped with security headers, rate limiting, and bearer token authentication.
// Mount at "/sse/".
// The SSE server registers two internal endpoints:
//   - /sse/sse — the SSE stream endpoint
//   - /sse/message — the message POST endpoint
//
// allowedHosts configures the Host-header allowlist; see NewMCPHandler for
// why mcp-go's built-in protection is disabled in favor of it.
func NewSSEHandler(token, name, version string, b *brain.Brain, embedder embeddings.Embedder, allowedHosts []string) http.Handler {
	mcpSrv := newMCPServer(name, version, b, embedder)
	sseTransport := server.NewSSEServer(mcpSrv,
		server.WithStaticBasePath("/sse"),
		server.WithSSEDisableLocalhostProtection(true),
	)
	return AllowedHosts(allowedHosts, SecureHeaders(RateLimit(mcpRequestsPerSecond, mcpBurstSize, BearerAuth(token, sseTransport))))
}
