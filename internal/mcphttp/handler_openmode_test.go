package mcphttp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/mcphttp"
)

// These tests drive the full composed middleware chain NewMCPHandler and
// NewSSEHandler build (AllowedHosts -> SecureHeaders -> RateLimit ->
// BearerAuth -> transport), not BearerAuth in isolation, to confirm none of
// the outer layers reintroduce a gate that BearerAuth's empty-token
// pass-through was supposed to remove.

// TestNewMCPHandler_EmptyToken_PassesThroughComposedChain proves an empty
// token clears the whole chain: a loopback request with no Authorization
// header reaches the transport (not 401 from BearerAuth, not 403 from
// AllowedHosts).
func TestNewMCPHandler_EmptyToken_PassesThroughComposedChain(t *testing.T) {
	b := brain.New(nil, nil, nil)
	handler := mcphttp.NewMCPHandler("", "openbrain", "0.1.0", b, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
		"empty token must not gate the composed /mcp chain")
	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"loopback host must still clear AllowedHosts with an empty token")
}

// TestNewSSEHandler_EmptyToken_PassesThroughComposedChain is the /sse/
// analog. The request targets the message sub-endpoint (synchronous 4xx on
// a missing sessionId) rather than the stream sub-endpoint, so the test
// cannot hang on a long-lived SSE connection.
func TestNewSSEHandler_EmptyToken_PassesThroughComposedChain(t *testing.T) {
	b := brain.New(nil, nil, nil)
	handler := mcphttp.NewSSEHandler("", "openbrain", "0.1.0", b, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/sse/message", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
		"empty token must not gate the composed /sse/ chain")
	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"loopback host must still clear AllowedHosts with an empty token")
}

// TestNewMCPHandler_EmptyToken_StillEnforcesAllowedHosts proves the
// AllowedHosts layer is untouched by the empty-token posture: a disallowed
// Host is still rejected even though BearerAuth itself would pass the
// request through.
func TestNewMCPHandler_EmptyToken_StillEnforcesAllowedHosts(t *testing.T) {
	b := brain.New(nil, nil, nil)
	handler := mcphttp.NewMCPHandler("", "openbrain", "0.1.0", b, nil, []string{"openbrain.wr-s.net"})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "evil.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"an unrecognized Host must still 403 even in open (empty-token) mode")
}
