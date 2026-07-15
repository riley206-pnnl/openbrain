package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
)

// testBrain returns a *brain.Brain suitable for exercising buildMux's route
// wiring. A nil pool/embedder is safe here because these tests assert auth
// gating and mount reachability, never a full brain dispatch that would touch
// the database.
func testBrain(cfg *config.Config) *brain.Brain {
	return brain.New(nil, nil, cfg)
}

func newMuxCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		WebHost: "127.0.0.1",
		WebPort: 10203,
	}
}

// ── staticAuth wiring on the real mux ────────────────────────────────────────
//
// These tests exercise the mux buildMux actually wires, not the bare handler
// functions in isolation, closing the gap where a route could be registered
// unwrapped (exactly the /api/ingest bug this fix pass addresses) with no
// test noticing.

// Every route staticAuth gates rejects a request with no ?token= when
// WebWSToken is set. Missing-token requests never reach the inner handler,
// so this is safe to run against every route including the DB-backed ones.
func TestBuildMux_WebRoutes_RequireTokenWhenSet(t *testing.T) {
	cfg := newMuxCfg(t)
	cfg.WebWSToken = validToken
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	for _, route := range webOpenRoutes {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, route, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code,
				"route %s should require ?token= when WebWSToken is set", route)
		})
	}
}

// routesReachingHandler excludes the routes whose handler dispatches
// straight into brain.Brain.Dispatch/GetStats with no request-shape-driven
// early return (/api/stats, /api/review both unconditionally call the DB
// pool). Those two routes are still covered by the missing-token 401 test
// above, which never reaches the inner handler; asserting acceptance would
// require a live database pool, which is out of scope for these mux-wiring
// tests.
func routesReachingHandler() []string {
	out := make([]string, 0, len(webOpenRoutes))
	for _, route := range webOpenRoutes {
		if route == "/api/stats" || route == "/api/review" {
			continue
		}
		out = append(out, route)
	}
	return out
}

// The same routes clear staticAuth with the correct ?token=. Requests are
// shaped to hit each handler's earliest validation branch (missing query
// param, wrong method) so the assertion proves auth passed without touching
// the (nil) database pool behind Brain.
func TestBuildMux_WebRoutes_AcceptCorrectToken(t *testing.T) {
	cfg := newMuxCfg(t)
	cfg.WebWSToken = validToken
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	for _, route := range routesReachingHandler() {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, route+"?token="+validToken, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			assert.NotEqual(t, http.StatusUnauthorized, rr.Code,
				"route %s should accept the correct ?token=", route)
		})
	}
}

// With WebWSToken empty, every route runs open: no request is rejected on
// auth grounds. This is the mux-level proof of the CRITICAL /api/ingest fix
// and the open-mode posture generally.
func TestBuildMux_WebRoutes_OpenWhenTokenEmpty(t *testing.T) {
	cfg := newMuxCfg(t)
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	for _, route := range routesReachingHandler() {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, route, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			assert.NotEqual(t, http.StatusUnauthorized, rr.Code,
				"route %s should run open when WebWSToken is empty", route)
		})
	}
}

// ── MCP transport mounting ───────────────────────────────────────────────────

// mcpMuxCfg returns a config with MCP HTTP enabled and a loopback allowlist
// so requests carrying a loopback Host clear AllowedHosts regardless of the
// configured public host list.
func mcpMuxCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := newMuxCfg(t)
	cfg.MCPHTTPEnabled = true
	return cfg
}

// newLoopbackRequest builds a request whose Host clears mcphttp.AllowedHosts
// via its always-permitted loopback branch, regardless of the configured
// allowlist.
func newLoopbackRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1"
	return req
}

// With MCPHTTPEnabled=true and MCPAuthToken empty, /mcp and /sse/ are
// mounted and reachable: a request reaches the transport (not a 404 from an
// absent route, not a 401 from BearerAuth) and runs open.
func TestBuildMux_MCPMount_OpenWhenTokenEmpty(t *testing.T) {
	cfg := mcpMuxCfg(t)
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	t.Run("/mcp", func(t *testing.T) {
		req := newLoopbackRequest(http.MethodPost, "/mcp")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.NotEqual(t, http.StatusNotFound, rr.Code, "/mcp should be mounted")
		assert.NotEqual(t, http.StatusUnauthorized, rr.Code, "/mcp should run open")
	})

	t.Run("/sse/message", func(t *testing.T) {
		// POST to the message sub-endpoint is synchronous (missing sessionId
		// is a fast 4xx); GET to the stream sub-endpoint (/sse/sse) would open
		// a long-lived SSE stream and is deliberately not exercised here.
		req := newLoopbackRequest(http.MethodPost, "/sse/message")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.NotEqual(t, http.StatusNotFound, rr.Code, "/sse/ should be mounted")
		assert.NotEqual(t, http.StatusUnauthorized, rr.Code, "/sse/ should run open")
	})
}

// With MCPHTTPEnabled=true and MCPAuthToken set, /mcp and /sse/ are gated:
// a request with no bearer token is rejected.
func TestBuildMux_MCPMount_GatedWhenTokenSet(t *testing.T) {
	cfg := mcpMuxCfg(t)
	cfg.MCPAuthToken = validToken
	cfg.OAuthIssuer = "https://openbrain.example.com"
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	t.Run("/mcp missing bearer", func(t *testing.T) {
		req := newLoopbackRequest(http.MethodPost, "/mcp")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("/sse/message missing bearer", func(t *testing.T) {
		req := newLoopbackRequest(http.MethodPost, "/sse/message")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("/mcp correct bearer clears auth", func(t *testing.T) {
		req := newLoopbackRequest(http.MethodPost, "/mcp")
		req.Header.Set("Authorization", "Bearer "+validToken)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		assert.NotEqual(t, http.StatusUnauthorized, rr.Code)
	})
}

// When MCP HTTP is disabled, /mcp and /sse/ are not registered at all: the
// mux's default handler serves 404, not an auth rejection.
func TestBuildMux_MCPMount_AbsentWhenDisabled(t *testing.T) {
	cfg := newMuxCfg(t)
	cfg.MCPHTTPEnabled = false
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	req := newLoopbackRequest(http.MethodPost, "/mcp")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ── OAuth endpoint mounting ───────────────────────────────────────────────────

var oauthRoutes = []string{
	"/.well-known/oauth-authorization-server",
	"/.well-known/oauth-protected-resource",
	"/authorize",
	"/register",
	"/token",
}

// The OAuth 2.0 endpoints mount only when MCPAuthToken is set: they are the
// authorization layer for an authenticated transport and have nothing to
// authorize in open mode.
func TestBuildMux_OAuthRoutes_AbsentWhenTokenEmpty(t *testing.T) {
	cfg := mcpMuxCfg(t)
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	for _, route := range oauthRoutes {
		t.Run(route, func(t *testing.T) {
			req := newLoopbackRequest(http.MethodGet, route)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusNotFound, rr.Code,
				"OAuth route %s must not mount when MCPAuthToken is empty", route)
		})
	}
}

func TestBuildMux_OAuthRoutes_PresentWhenTokenSet(t *testing.T) {
	cfg := mcpMuxCfg(t)
	cfg.MCPAuthToken = validToken
	cfg.OAuthIssuer = "https://openbrain.example.com"
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	for _, route := range oauthRoutes {
		t.Run(route, func(t *testing.T) {
			req := newLoopbackRequest(http.MethodGet, route)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			assert.NotEqual(t, http.StatusNotFound, rr.Code,
				"OAuth route %s must mount when MCPAuthToken is set", route)
		})
	}
}

// ── /health auth_mode ────────────────────────────────────────────────────────

func TestHealthHandler_ReportsAuthMode(t *testing.T) {
	cfg := newMuxCfg(t)
	cfg.WebWSToken = validToken
	// MCPAuthToken left empty: web required, mcp open.
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Status   string            `json:"status"`
		AuthMode map[string]string `json:"auth_mode"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "ok", body.Status)
	assert.Equal(t, "required", body.AuthMode["web"])
	assert.Equal(t, "open", body.AuthMode["mcp"])
	assert.NotContains(t, rr.Body.String(), validToken, "the /health response must never leak the token value")
}

func TestHealthHandler_ReportsOpenForBothWhenUnset(t *testing.T) {
	cfg := newMuxCfg(t)
	mux, err := buildMux(cfg, testBrain(cfg), nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		AuthMode map[string]string `json:"auth_mode"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "open", body.AuthMode["web"])
	assert.Equal(t, "open", body.AuthMode["mcp"])
}

// ── open-mode startup warnings actually fire ─────────────────────────────────

// capturingSlogHandler records every record it receives so tests can assert
// a warning fired without depending on log output formatting.
type capturingSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingSlogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingSlogHandler) WithGroup(_ string) slog.Handler      { return h }

// attrMap flattens a slog.Record's attrs into a map for assertions.
func attrMap(r slog.Record) map[string]any {
	m := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	return m
}

func withCapturedLogs(t *testing.T, fn func()) []slog.Record {
	t.Helper()
	prev := slog.Default()
	h := &capturingSlogHandler{}
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(prev)

	fn()

	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// buildMux must actually emit the loud open-mode warning when WebWSToken is
// empty, not merely leave the surface open with no operator-visible signal.
func TestBuildMux_EmitsWebOpenModeWarning(t *testing.T) {
	cfg := newMuxCfg(t)

	records := withCapturedLogs(t, func() {
		_, err := buildMux(cfg, testBrain(cfg), nil)
		require.NoError(t, err)
	})

	var found *slog.Record
	for i := range records {
		if records[i].Level == slog.LevelWarn && records[i].Message == "web auth token unset; web surface running OPEN with no authentication" {
			found = &records[i]
			break
		}
	}
	require.NotNil(t, found, "expected the web open-mode warning to fire")

	attrs := attrMap(*found)
	assert.Equal(t, cfg.WebAddr(), attrs["bind"])
	assert.ElementsMatch(t, webOpenRoutes, attrs["open_routes"], "open_routes payload must list every gated route")
	assert.ElementsMatch(t, webOpenWriteEndpoints, attrs["open_write_endpoints"])
}

// The web open-mode warning must NOT fire when WebWSToken is set.
func TestBuildMux_NoWebOpenModeWarning_WhenTokenSet(t *testing.T) {
	cfg := newMuxCfg(t)
	cfg.WebWSToken = validToken

	records := withCapturedLogs(t, func() {
		_, err := buildMux(cfg, testBrain(cfg), nil)
		require.NoError(t, err)
	})

	for _, r := range records {
		assert.NotEqual(t, "web auth token unset; web surface running OPEN with no authentication", r.Message)
	}
}

// buildMux must emit the loud open-mode warning for the MCP transport when
// MCPHTTPEnabled is true and MCPAuthToken is empty.
func TestBuildMux_EmitsMCPOpenModeWarning(t *testing.T) {
	cfg := mcpMuxCfg(t)
	cfg.WebWSToken = validToken // isolate: only the MCP warning should fire

	records := withCapturedLogs(t, func() {
		_, err := buildMux(cfg, testBrain(cfg), nil)
		require.NoError(t, err)
	})

	var found *slog.Record
	for i := range records {
		if records[i].Level == slog.LevelWarn && records[i].Message == "MCP HTTP transport running OPEN; OPENBRAIN_MCP_AUTH_TOKEN unset, /mcp and /sse/ accept unauthenticated requests" {
			found = &records[i]
			break
		}
	}
	require.NotNil(t, found, "expected the MCP open-mode warning to fire")

	attrs := attrMap(*found)
	assert.Equal(t, cfg.WebAddr(), attrs["bind"])
}

// The MCP open-mode warning must NOT fire when MCPAuthToken is set.
func TestBuildMux_NoMCPOpenModeWarning_WhenTokenSet(t *testing.T) {
	cfg := mcpMuxCfg(t)
	cfg.MCPAuthToken = validToken
	cfg.OAuthIssuer = "https://openbrain.example.com"

	records := withCapturedLogs(t, func() {
		_, err := buildMux(cfg, testBrain(cfg), nil)
		require.NoError(t, err)
	})

	for _, r := range records {
		assert.NotEqual(t, "MCP HTTP transport running OPEN; OPENBRAIN_MCP_AUTH_TOKEN unset, /mcp and /sse/ accept unauthenticated requests", r.Message)
	}
}
