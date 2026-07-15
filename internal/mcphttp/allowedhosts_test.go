package mcphttp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/mcphttp"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestAllowedHosts_AcceptsConfiguredPublicHost covers the OB-054 regression:
// mcp-go v0.56.0's built-in DNS-rebinding protection 403'd every remote
// request because openbrain-web binds a loopback address and cloudflared
// forwards "Host: openbrain.wr-s.net" unchanged. The allowlist must accept a
// Host it was explicitly configured with.
func TestAllowedHosts_AcceptsConfiguredPublicHost(t *testing.T) {
	handler := mcphttp.AllowedHosts([]string{"openbrain.wr-s.net"}, okHandler())

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "openbrain.wr-s.net"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "configured public Host must be accepted, not 403'd")
}

// TestAllowedHosts_RejectsUnknownHost verifies the allowlist still rejects a
// Host that was never named, preserving the DNS-rebinding protection intent.
func TestAllowedHosts_RejectsUnknownHost(t *testing.T) {
	handler := mcphttp.AllowedHosts([]string{"openbrain.wr-s.net"}, okHandler())

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "evil.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid Host header")
}

// TestAllowedHosts_LoopbackAlwaysAllowed verifies loopback Host headers pass
// regardless of the configured allowlist, so local tooling and health checks
// are unaffected by this change.
func TestAllowedHosts_LoopbackAlwaysAllowed(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"bare 127.0.0.1", "127.0.0.1"},
		{"127.0.0.1 with port", "127.0.0.1:10203"},
		{"localhost", "localhost"},
		{"localhost with port", "localhost:10203"},
		{"IPv6 loopback with port", "[::1]:10203"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Deliberately configure an allowlist that does NOT include
			// loopback, to prove loopback access does not depend on it.
			handler := mcphttp.AllowedHosts([]string{"openbrain.wr-s.net"}, okHandler())

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// TestAllowedHosts_RejectsSubdomainSpoof verifies a Host that merely contains
// an allowed name as a substring (a classic allowlist-bypass shape) is still
// rejected: the comparison is an exact match on the host portion, not a
// substring/suffix match.
func TestAllowedHosts_RejectsSubdomainSpoof(t *testing.T) {
	handler := mcphttp.AllowedHosts([]string{"openbrain.wr-s.net"}, okHandler())

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "openbrain.wr-s.net.evil.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAllowedHosts_EmptyAllowlistStillAllowsLoopback verifies a nil/empty
// configured allowlist (e.g. OPENBRAIN_MCP_ALLOWED_HOSTS unset with no
// default) still lets loopback through, and rejects everything else.
func TestAllowedHosts_EmptyAllowlistStillAllowsLoopback(t *testing.T) {
	handler := mcphttp.AllowedHosts(nil, okHandler())

	loopbackReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	loopbackReq.Host = "127.0.0.1:10203"
	loopbackRec := httptest.NewRecorder()
	handler.ServeHTTP(loopbackRec, loopbackReq)
	assert.Equal(t, http.StatusOK, loopbackRec.Code)

	remoteReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	remoteReq.Host = "openbrain.wr-s.net"
	remoteRec := httptest.NewRecorder()
	handler.ServeHTTP(remoteRec, remoteReq)
	assert.Equal(t, http.StatusForbidden, remoteRec.Code)
}

// TestNewMCPHandler_HostAllowlist wires AllowedHosts through the full
// NewMCPHandler chain (SecureHeaders -> RateLimit -> BearerAuth ->
// AllowedHosts -> transport) to prove the regression is fixed end to end: a
// disallowed Host is rejected with 403 before authentication is even
// evaluated, while the configured public Host and loopback both clear the
// Host check and reach the auth layer (401, not 403, with no token supplied).
func TestNewMCPHandler_HostAllowlist(t *testing.T) {
	b := brain.New(nil, nil, nil)
	handler := mcphttp.NewMCPHandler("test-secret-token", "openbrain", "0.1.0", b, nil,
		[]string{"openbrain.wr-s.net"})

	t.Run("disallowed host is rejected before auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Host = "evil.com"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code,
			"an unrecognized Host must 403 even with no Authorization header, proving the host check runs before auth")
	})

	t.Run("configured public host clears the host check", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Host = "openbrain.wr-s.net"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		// No Authorization header is supplied, so the request must fail auth
		// (401), not the host check (403). A 401 here is the proof the OB-054
		// regression is fixed: this exact Host used to be unconditionally 403'd.
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("loopback host clears the host check", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Host = "127.0.0.1:10203"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
