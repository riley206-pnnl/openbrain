package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/embeddings"
	"github.com/windingriverholdings/openbrain/internal/intent"
	"github.com/windingriverholdings/openbrain/internal/mcphttp"
)

//go:embed static
var staticFS embed.FS

// newUpgrader creates a WebSocket upgrader with origin validation.
// If allowedOrigins is empty, only same-origin requests are allowed.
func newUpgrader(allowedOrigins string) websocket.Upgrader {
	allowed := parseAllowedOrigins(allowedOrigins)
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // no Origin header — same-origin or non-browser client
			}
			if len(allowed) == 0 {
				// Default: only allow if origin matches the Host header
				return origin == "http://"+r.Host || origin == "https://"+r.Host
			}
			for _, a := range allowed {
				if strings.EqualFold(origin, a) {
					return true
				}
			}
			return false
		},
	}
}

// parseAllowedOrigins splits a comma-separated origin list into a slice.
func parseAllowedOrigins(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func serveHTTP(ctx context.Context, cfg *config.Config, b *brain.Brain, embedder embeddings.Embedder) error {
	mux := http.NewServeMux()

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("static fs: %w", err)
	}
	mux.Handle("/", staticAuth(cfg.WebWSToken, http.FileServer(http.FS(staticSub))))
	mux.Handle("/graph", staticAuth(cfg.WebWSToken, graphHandler(staticSub)))
	mux.Handle("/brain.json", staticAuth(cfg.WebWSToken, brainJSONHandler(staticSub, cfg.VizOutputPath)))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	mux.Handle("/api/rebuild-viz", staticAuth(cfg.WebWSToken, apiRebuildViz(cfg)))
	mux.Handle("/api/search", staticAuth(cfg.WebWSToken, apiSearch(b)))
	mux.Handle("/api/search/nodes", staticAuth(cfg.WebWSToken, apiSearchNodes(b)))
	mux.Handle("/api/thought/", staticAuth(cfg.WebWSToken, apiGetThought(b)))
	mux.Handle("/api/capture", staticAuth(cfg.WebWSToken, apiCapture(b)))
	mux.Handle("/api/stats", staticAuth(cfg.WebWSToken, apiStats(b)))
	mux.Handle("/api/review", staticAuth(cfg.WebWSToken, apiReview(b)))
	mux.Handle("/api/ingest", staticAuth(cfg.WebWSToken, apiIngest(b, cfg)))

	upgrader := newUpgrader(cfg.WebAllowedOrigins)
	mux.HandleFunc("/ws", wsHandler(b, upgrader, cfg.WebWSToken))
	if cfg.WebWSToken == "" {
		slog.Warn("running without authentication: /ws and all HTTP routes are open, including the write endpoints /api/capture and /api/ingest, and the data endpoints /, /graph, /brain.json, /api/search, /api/search/nodes, /api/thought/, /api/stats, /api/review, /api/rebuild-viz; set OPENBRAIN_WEB_WS_TOKEN to enable")
	}

	// Mount MCP HTTP transports when enabled
	if cfg.MCPHTTPEnabled && cfg.MCPAuthToken != "" {
		slog.Info("mounting MCP HTTP transport", "endpoints", []string{"/mcp", "/sse/"})
		mux.Handle("/mcp", mcphttp.NewMCPHandler(cfg.MCPAuthToken, cfg.MCPServerName, cfg.MCPServerVersion, b, embedder))
		mux.Handle("/sse/", mcphttp.NewSSEHandler(cfg.MCPAuthToken, cfg.MCPServerName, cfg.MCPServerVersion, b, embedder))

		// Mount OAuth 2.0 endpoints for MCP spec compliance.
		// The MCP spec (2025-03-26) requires authorization code flow with PKCE.
		// Claude.ai's web MCP connector uses fallback paths (/authorize, /token,
		// /register) regardless of what the metadata advertises.
		slog.Info("mounting OAuth 2.0 endpoints",
			"endpoints", []string{
				"/.well-known/oauth-authorization-server",
				"/.well-known/oauth-protected-resource",
				"/authorize",
				"/register",
				"/token",
			})
		mux.HandleFunc("/.well-known/oauth-authorization-server",
			mcphttp.OAuthMetadataHandler(cfg.OAuthIssuer))
		mux.HandleFunc("/.well-known/oauth-protected-resource",
			mcphttp.ProtectedResourceHandler(cfg.OAuthIssuer))

		// Authorization endpoint: auto-approves and redirects with code (PKCE).
		mux.HandleFunc("/authorize", mcphttp.AuthorizeHandler())

		// Dynamic Client Registration (RFC 7591): Claude.ai registers before auth.
		mux.Handle("/register",
			mcphttp.SecureHeaders(
				mcphttp.RateLimit(0.083, 3,
					mcphttp.RegisterHandler())))

		// Token endpoint: supports authorization_code grant (PKCE).
		// Rate-limited aggressively (5 req/min = 0.083 rps, burst 3).
		mux.Handle("/token",
			mcphttp.SecureHeaders(
				mcphttp.RateLimit(0.083, 3,
					mcphttp.AuthCodeTokenHandler(cfg.MCPAuthToken))))

		// Legacy token endpoint for client_credentials grant.
		// Kept for backward compatibility with existing integrations.
		if cfg.OAuthClientID != "" && cfg.OAuthClientSecret != "" {
			mux.Handle("/oauth/token",
				mcphttp.SecureHeaders(
					mcphttp.RateLimit(0.083, 3,
						mcphttp.OAuthTokenHandler(cfg.OAuthClientID, cfg.OAuthClientSecret, cfg.MCPAuthToken))))
		}
	} else if cfg.MCPHTTPEnabled {
		slog.Warn("MCP HTTP transport enabled but OPENBRAIN_MCP_AUTH_TOKEN is empty; transport NOT mounted")
	}

	srv := &http.Server{Addr: cfg.WebAddr(), Handler: mux}

	// Graceful shutdown on context cancellation
	go func() {
		<-ctx.Done()
		slog.Info("shutting down web server")
		srv.Shutdown(context.Background())
	}()

	err = srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func apiSearch(b *brain.Brain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "missing q parameter", http.StatusBadRequest)
			return
		}

		parsed := intent.ParsedIntent{Intent: intent.Search, Text: query, ThoughtType: "note"}
		result, err := b.Dispatch(r.Context(), parsed, "web")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		jsonResponse(w, map[string]string{"result": result})
	}
}

// apiSearchNodes returns search results as a JSON array with full node metadata
// (id, score, type, tags, summary, content) so the graph page can highlight
// matching nodes by their UUID. Unlike /api/search it does not format results
// as a human-readable string.
func apiSearchNodes(b *brain.Brain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			http.Error(w, "missing q parameter", http.StatusBadRequest)
			return
		}

		opts := brain.SearchOpts{Mode: "hybrid"}

		if from := r.URL.Query().Get("from"); from != "" {
			t, err := time.Parse("2006-01-02", from)
			if err != nil {
				http.Error(w, "invalid from date: use YYYY-MM-DD", http.StatusBadRequest)
				return
			}
			opts.CreatedFrom = &t
		}
		if to := r.URL.Query().Get("to"); to != "" {
			t, err := time.Parse("2006-01-02", to)
			if err != nil {
				http.Error(w, "invalid to date: use YYYY-MM-DD", http.StatusBadRequest)
				return
			}
			// Use end-of-day so the to-date is inclusive.
			eod := t.Add(24*time.Hour - time.Nanosecond)
			opts.CreatedTo = &eod
		}

		rows, err := b.Search(r.Context(), query, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		type nodeResult struct {
			ID        string   `json:"id"`
			Score     float64  `json:"score"`
			Type      string   `json:"type"`
			Tags      []string `json:"tags"`
			Summary   string   `json:"summary"`
			Content   string   `json:"content"`
			CreatedAt string   `json:"created_at"`
		}

		results := make([]nodeResult, 0, len(rows))
		for _, row := range rows {
			summary := ""
			if row.Summary != nil {
				summary = *row.Summary
			}
			score := 0.0
			if row.Score != nil {
				score = *row.Score
			}
			// Truncate content for the panel preview; full text is in the tooltip.
			content := row.Content
			if len(content) > 200 {
				content = content[:200] + "…"
			}
			tags := row.Tags
			if tags == nil {
				tags = []string{}
			}
			results = append(results, nodeResult{
				ID:        row.ID,
				Score:     score,
				Type:      row.ThoughtType,
				Tags:      tags,
				Summary:   summary,
				Content:   content,
				CreatedAt: row.CreatedAt.Format("2006-01-02"),
			})
		}

		jsonResponse(w, results)
	}
}

// apiGetThought returns a single thought by UUID for the detail panel.
// Route: GET /api/thought/{uuid}
func apiGetThought(b *brain.Brain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/thought/")
		if id == "" {
			http.Error(w, "missing thought id", http.StatusBadRequest)
			return
		}

		thought, err := b.GetThought(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if thought == nil {
			http.Error(w, "thought not found", http.StatusNotFound)
			return
		}

		type thoughtDetail struct {
			ID          string   `json:"id"`
			Type        string   `json:"type"`
			Tags        []string `json:"tags"`
			Source      string   `json:"source"`
			Summary     string   `json:"summary"`
			Content     string   `json:"content"`
			CreatedAt   string   `json:"created_at"`
		}

		summary := ""
		if thought.Summary != nil {
			summary = *thought.Summary
		}
		tags := thought.Tags
		if tags == nil {
			tags = []string{}
		}
		jsonResponse(w, thoughtDetail{
			ID:        thought.ID,
			Type:      thought.ThoughtType,
			Tags:      tags,
			Source:    thought.Source,
			Summary:   summary,
			Content:   thought.Content,
			CreatedAt: thought.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
}

func apiCapture(b *brain.Brain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Content     string   `json:"content"`
			ThoughtType string   `json:"thought_type"`
			Tags        []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		if body.ThoughtType == "" {
			body.ThoughtType = intent.InferType(body.Content)
		}

		parsed := intent.ParsedIntent{
			Intent:      intent.Capture,
			Text:        body.Content,
			ThoughtType: body.ThoughtType,
			Tags:        body.Tags,
		}
		result, err := b.Dispatch(r.Context(), parsed, "web")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		jsonResponse(w, map[string]string{"result": result})
	}
}

func apiStats(b *brain.Brain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parsed := intent.ParsedIntent{Intent: intent.Stats, Text: "stats", ThoughtType: "note"}
		result, err := b.Dispatch(r.Context(), parsed, "web")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"result": result})
	}
}

func apiReview(b *brain.Brain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		days := 7
		if d := r.URL.Query().Get("days"); d != "" {
			if n, err := strconv.Atoi(d); err == nil && n > 0 {
				days = n
			}
		}
		_ = days // TODO: pass configurable days to brain.GetReview

		parsed := intent.ParsedIntent{Intent: intent.Review, Text: "review", ThoughtType: "note"}
		result, err := b.Dispatch(r.Context(), parsed, "web")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"result": result})
	}
}

type wsMessage struct {
	Message string `json:"message"`
}

type wsResponse struct {
	Content     string `json:"content"`
	Intent      string `json:"intent"`
	ThoughtType string `json:"thought_type"`
}

// staticAuth wraps a handler so that, when authToken is non-empty, requests must
// carry the token via the ?token= query parameter (the same mechanism used by
// wsHandler).  When authToken is empty the handler is passed through unchanged.
func staticAuth(authToken string, next http.Handler) http.Handler {
	if authToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qToken := r.URL.Query().Get("token")
		if subtle.ConstantTimeCompare([]byte(qToken), []byte(authToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// graphHandler serves graph.html at the /graph route without requiring the .html
// suffix in the URL.
func graphHandler(staticSub fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, staticSub, "graph.html")
	})
}

func wsHandler(b *brain.Brain, upgrader websocket.Upgrader, authToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// When an auth token is configured, require it via query param.
		// WebSocket connections cannot send custom headers from browsers,
		// so the token is passed as ?token=<value>.
		if authToken != "" {
			qToken := r.URL.Query().Get("token")
			if subtle.ConstantTimeCompare([]byte(qToken), []byte(authToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("websocket upgrade failed", "error", err)
			return
		}
		defer conn.Close()

		for {
			var msg wsMessage
			if err := conn.ReadJSON(&msg); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					slog.Error("websocket read error", "error", err)
				}
				return
			}

			parsed := intent.Parse(msg.Message)
			result, err := b.Dispatch(r.Context(), parsed, "web")
			if err != nil {
				result = fmt.Sprintf("Error: %v", err)
			}

			resp := wsResponse{
				Content:     result,
				Intent:      string(parsed.Intent),
				ThoughtType: parsed.ThoughtType,
			}
			if err := conn.WriteJSON(resp); err != nil {
				slog.Error("websocket write error", "error", err)
				return
			}
		}
	}
}

func jsonResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// brainJSONHandler serves brain.json from disk when vizOutputPath is set,
// falling back to the embedded copy otherwise. Serving from disk lets the
// rebuild endpoint update the file without restarting the server.
func brainJSONHandler(staticSub fs.FS, vizOutputPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if vizOutputPath != "" {
			data, err := os.ReadFile(vizOutputPath)
			if err == nil {
				w.Header().Set("Content-Type", "application/json")
				w.Write(data)
				return
			}
			slog.Warn("brain.json: disk read failed, falling back to embedded", "path", vizOutputPath, "error", err)
		}
		http.ServeFileFS(w, r, staticSub, "brain.json")
	})
}

// apiRebuildViz runs the build-brain-viz.py script to regenerate brain.json.
// Route: POST /api/rebuild-viz
// Auth: handled by staticAuth at registration time.
// Requires OPENBRAIN_VIZ_SCRIPT_PATH and OPENBRAIN_VIZ_OUTPUT_PATH to be set.
func apiRebuildViz(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if cfg.VizScriptPath == "" || cfg.VizOutputPath == "" {
			http.Error(w, "OPENBRAIN_VIZ_SCRIPT_PATH and OPENBRAIN_VIZ_OUTPUT_PATH must be set to enable rebuild", http.StatusServiceUnavailable)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()

		cmd := exec.CommandContext(ctx, "python3", cfg.VizScriptPath, "--output", cfg.VizOutputPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Error("rebuild-viz failed", "error", err, "output", string(out))
			http.Error(w, "rebuild failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		slog.Info("rebuild-viz succeeded", "output_path", cfg.VizOutputPath)
		jsonResponse(w, map[string]string{"status": "ok"})
	}
}
