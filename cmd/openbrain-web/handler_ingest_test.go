package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/config"
)

// fakeIngester records calls to IngestDocument so handler tests can verify
// that the handler reaches the brain layer with the expected arguments —
// without needing a live database or embedder.
type fakeIngester struct {
	calls      int
	lastPath   string
	lastSource string
	lastAuto   bool
	result     string
	err        error
}

func (f *fakeIngester) IngestDocument(_ context.Context, filePath, source string, autoCapture bool, _ ...map[string]any) (string, error) {
	f.calls++
	f.lastPath = filePath
	f.lastSource = source
	f.lastAuto = autoCapture
	if f.err != nil {
		return "", f.err
	}
	return f.result, nil
}

func newIngestCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		IngestDir:      t.TempDir(),
		IngestMaxBytes: 1 << 20, // 1 MB cap for tests
	}
}

func buildMultipart(t *testing.T, filename, content, source string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if filename != "" {
		fw, err := w.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = io.Copy(fw, strings.NewReader(content))
		require.NoError(t, err)
	}
	if source != "" {
		require.NoError(t, w.WriteField("source", source))
	}
	require.NoError(t, w.Close())
	return &body, w.FormDataContentType()
}

func TestApiIngest_RejectsWrongMethod(t *testing.T) {
	cfg := newIngestCfg(t)
	h := apiIngest(&fakeIngester{}, cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/ingest", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApiIngest_RejectsMissingFile(t *testing.T) {
	cfg := newIngestCfg(t)
	h := apiIngest(&fakeIngester{}, cfg)
	body, ct := buildMultipart(t, "", "", "manual")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// Auth is handled entirely by staticAuth at registration time (see
// serveHTTP), identical to every other write endpoint. These tests exercise
// the same route wrapping used in production to prove the CRITICAL fix: an
// empty WebWSToken leaves /api/ingest genuinely open (it previously 401'd
// unconditionally via an independent, now-removed Authorization: Bearer
// check against MCPAuthToken), and a set WebWSToken requires the ?token=
// query param exactly like its siblings.

func TestApiIngestRoute_EmptyWebWSToken_EmptyMCPAuthToken_Open(t *testing.T) {
	cfg := newIngestCfg(t)
	// Both secrets unset: the historical bug rejected every request here
	// even though the web surface is meant to be open in this configuration.
	require.Empty(t, cfg.WebWSToken)
	require.Empty(t, cfg.MCPAuthToken)

	ing := &fakeIngester{result: "ok"}
	route := staticAuth(cfg.WebWSToken, apiIngest(ing, cfg))
	body, ct := buildMultipart(t, "note.txt", "hello", "")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	route.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, 1, ing.calls)
}

func TestApiIngestRoute_SetWebWSToken_MissingQueryToken_Rejected(t *testing.T) {
	cfg := newIngestCfg(t)
	cfg.WebWSToken = validToken

	route := staticAuth(cfg.WebWSToken, apiIngest(&fakeIngester{}, cfg))
	body, ct := buildMultipart(t, "note.txt", "hello", "")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	route.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestApiIngestRoute_SetWebWSToken_WrongQueryToken_Rejected(t *testing.T) {
	cfg := newIngestCfg(t)
	cfg.WebWSToken = validToken

	route := staticAuth(cfg.WebWSToken, apiIngest(&fakeIngester{}, cfg))
	body, ct := buildMultipart(t, "note.txt", "hello", "")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest?token=wrong-token", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	route.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestApiIngestRoute_SetWebWSToken_CorrectQueryToken_Accepted(t *testing.T) {
	cfg := newIngestCfg(t)
	cfg.WebWSToken = validToken

	ing := &fakeIngester{result: "ok"}
	route := staticAuth(cfg.WebWSToken, apiIngest(ing, cfg))
	body, ct := buildMultipart(t, "note.txt", "hello", "")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest?token="+validToken, body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	route.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, 1, ing.calls)
}

func TestApiIngest_NeutralizesPathTraversalInFilename(t *testing.T) {
	// Go's multipart parser already runs filepath.Base on the
	// Content-Disposition filename, so traversal payloads arrive at the
	// handler with their directory parts stripped. The safety property we
	// care about is the *outcome*: no matter what the client sends, the
	// staged file lands inside IngestDir/.uploads/ with no escape.
	cfg := newIngestCfg(t)
	ing := &fakeIngester{result: "ok"}
	h := apiIngest(ing, cfg)
	body, ct := buildMultipart(t, "../../etc/passwd", "evil", "")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1, ing.calls)

	uploadsDir := filepath.Join(cfg.IngestDir, ".uploads") + string(filepath.Separator)
	assert.True(t, strings.HasPrefix(ing.lastPath, uploadsDir),
		"staged file must not escape IngestDir/.uploads/, got %s", ing.lastPath)
	assert.True(t, strings.HasSuffix(ing.lastPath, "passwd"),
		"staged filename should end with the stripped basename 'passwd', got %s", ing.lastPath)
	assert.NotContains(t, ing.lastPath, "..", "no parent-dir references should survive")
}

func TestApiIngest_RejectsOversize(t *testing.T) {
	cfg := newIngestCfg(t)
	cfg.IngestMaxBytes = 10 // tiny cap
	h := apiIngest(&fakeIngester{}, cfg)
	body, ct := buildMultipart(t, "note.txt", strings.Repeat("x", 4096), "")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h(rr, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

func TestApiIngest_HappyPath(t *testing.T) {
	cfg := newIngestCfg(t)
	ing := &fakeIngester{result: "Ingested note.txt: 1 thought captured"}
	h := apiIngest(ing, cfg)
	body, ct := buildMultipart(t, "note.txt", "hello world", "laptop")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1, ing.calls, "IngestDocument must be called exactly once")
	assert.Equal(t, "laptop", ing.lastSource)
	assert.True(t, ing.lastAuto, "auto_capture default should be true")

	uploadsDir := filepath.Join(cfg.IngestDir, ".uploads") + string(filepath.Separator)
	assert.True(t, strings.HasPrefix(ing.lastPath, uploadsDir),
		"staged file should live inside IngestDir/.uploads/, got %s", ing.lastPath)
	assert.True(t, strings.HasSuffix(ing.lastPath, "note.txt"),
		"staged filename should preserve the basename (for format detection), got %s", ing.lastPath)

	_, err := os.Stat(ing.lastPath)
	assert.True(t, os.IsNotExist(err),
		"temp file must be cleaned up after handler returns, but it still exists at %s", ing.lastPath)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Contains(t, resp["result"], "1 thought captured")
}

func TestApiIngest_DefaultsSourceWhenOmitted(t *testing.T) {
	cfg := newIngestCfg(t)
	ing := &fakeIngester{result: "ok"}
	h := apiIngest(ing, cfg)
	body, ct := buildMultipart(t, "note.txt", "hello", "")
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "http-upload", ing.lastSource,
		"source should default to a stable identifier when caller omits it")
}
