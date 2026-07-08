package brain

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ErrEmptyText means text destined for the embedding backend was empty or
// whitespace-only. Capture, Search, and Supersede all refuse it here, before
// any embed call is made, so the resulting error is unambiguously an
// input-validation failure. It is never confused with a genuine
// empty-embedding response from the backend itself (see
// embeddings.OllamaEmbedder.Embed, whose "ollama returned empty embedding"
// error is reserved for that distinct, backend-attributed case).
var ErrEmptyText = errors.New("text is empty")

// requireNonEmptyText guards against empty or whitespace-only text reaching
// an embedder call. op names the calling operation (e.g. "capture",
// "search"), used only in the error message and the server log; the text
// itself is never logged, only its length.
func requireNonEmptyText(op, text string) error {
	if strings.TrimSpace(text) != "" {
		return nil
	}
	slog.Warn("rejected empty input before embed call", "op", op, "text_len", len(text))
	return fmt.Errorf("%s: %w", op, ErrEmptyText)
}
