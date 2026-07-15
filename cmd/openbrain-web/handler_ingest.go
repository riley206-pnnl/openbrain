package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/windingriverholdings/openbrain/internal/config"
)

// docIngester is the subset of brain.Brain that the upload handler needs.
// Defined as an interface so tests can substitute a fake without spinning up
// a real database or embedder.
type docIngester interface {
	IngestDocument(ctx context.Context, filePath, source string, autoCapture bool, callerMeta ...map[string]any) (string, error)
}

// uploadStageSubdir is the dot-prefixed directory inside IngestDir where
// inbound uploads are staged before being handed to the brain. The leading
// dot keeps it out of accidental folder watches and visually distinguishes
// transient upload state from operator-curated ingest content.
const uploadStageSubdir = ".uploads"

// defaultUploadSource is recorded against an ingested file when the client
// omits the `source` form field.
const defaultUploadSource = "http-upload"

// apiIngest accepts a multipart file upload, stages it inside the server's
// IngestDir, hands it to the brain for parsing/embedding/storage, and
// removes the staged file before returning.
//
// The handler exists so that clients on machines other than the server
// (e.g. a laptop) can ingest documents into a shared brain without
// shipping file bytes through the MCP/JSON-RPC channel — which would
// otherwise blow up the calling agent's token budget for any non-trivial
// document. The bytes flow over plain HTTPS; only the result summary is
// returned over MCP if the caller relays this through an agent.
//
// Request: POST /api/ingest, multipart/form-data
//
//	file         (required) the document
//	source       (optional) caller identifier, defaults to "http-upload"
//	auto_capture (optional) "false" disables deep-capture extraction
//
// Auth: handled by staticAuth at registration time, identical to its sibling
// write endpoints (/api/capture, /api/rebuild-viz). /api/ingest previously
// carried its own independent Authorization: Bearer check against
// cfg.MCPAuthToken; that check was removed because it double-gated the route
// on a second, unrelated secret and, when MCPAuthToken was also unset,
// unconditionally rejected every request even with WebWSToken empty — which
// contradicted the open-mode posture the rest of the web surface uses.
func apiIngest(b docIngester, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		maxBytes := cfg.IngestMaxBytes
		if maxBytes <= 0 {
			maxBytes = config.DefaultIngestMaxBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

		if err := r.ParseMultipartForm(8 << 20); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "request body exceeds OPENBRAIN_INGEST_MAX_BYTES", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file field", http.StatusBadRequest)
			return
		}
		defer file.Close()

		basename, err := sanitizeUploadFilename(header.Filename)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		source := strings.TrimSpace(r.FormValue("source"))
		if source == "" {
			source = defaultUploadSource
		}
		autoCapture := true
		if v := strings.TrimSpace(r.FormValue("auto_capture")); v != "" {
			parsed, parseErr := strconv.ParseBool(v)
			if parseErr != nil {
				http.Error(w, "invalid auto_capture value: must be true or false", http.StatusBadRequest)
				return
			}
			autoCapture = parsed
		}

		stagedPath, err := stageUpload(cfg.IngestDir, basename, file)
		if err != nil {
			slog.Error("ingest: stage upload failed", "error", err)
			http.Error(w, "failed to stage upload", http.StatusInternalServerError)
			return
		}
		defer func() {
			if rmErr := os.Remove(stagedPath); rmErr != nil && !os.IsNotExist(rmErr) {
				slog.Warn("ingest: failed to remove staged upload", "path", stagedPath, "error", rmErr)
			}
		}()

		result, err := b.IngestDocument(r.Context(), stagedPath, source, autoCapture)
		if err != nil {
			slog.Error("ingest: brain rejected document", "error", err)
			http.Error(w, "ingestion failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"result": result})
	}
}

// sanitizeUploadFilename returns just the basename of the caller-supplied
// filename. Go's multipart parser already runs filepath.Base on the value
// from the Content-Disposition header (see mime/multipart Part.FileName),
// but we run it again so the safety doesn't depend on undocumented
// upstream behavior. The basename is needed because docparse keys on the
// file extension to pick a parser.
func sanitizeUploadFilename(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("missing filename")
	}
	base := filepath.Base(raw)
	if base == "." || base == ".." || base == string(filepath.Separator) {
		return "", errors.New("invalid filename")
	}
	if strings.ContainsAny(base, "/\\\x00") {
		return "", errors.New("filename must not contain path separators or null bytes")
	}
	return base, nil
}

// stageUpload copies the request body to a fresh file inside
// <ingestDir>/.uploads/, naming it <hex>-<basename> so collisions across
// concurrent uploads are impossible and the extension is preserved for the
// document parser. Returns the absolute staged path.
func stageUpload(ingestDir, basename string, body io.Reader) (string, error) {
	if ingestDir == "" {
		return "", errors.New("OPENBRAIN_INGEST_DIR not configured")
	}
	stageDir := filepath.Join(ingestDir, uploadStageSubdir)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return "", err
	}

	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	staged := filepath.Join(stageDir, hex.EncodeToString(rnd[:])+"-"+basename)

	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, body); err != nil {
		out.Close()
		os.Remove(staged)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(staged)
		return "", err
	}
	return staged, nil
}
