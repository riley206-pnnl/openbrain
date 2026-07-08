package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/windingriverholdings/openbrain/internal/chunker"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/docparse"
	"github.com/windingriverholdings/openbrain/internal/extract"
	"github.com/windingriverholdings/openbrain/internal/intent"
)

func TestIngestDocument_DetectsFormat(t *testing.T) {
	// IngestDocument should detect format from file extension and parse.
	// Use a real PDF fixture from docparse testdata.
	dir := t.TempDir()
	cfg := &config.Config{IngestDir: dir}
	b := New(nil, nil, cfg)

	// Copy sample.pdf to temp dir
	src := filepath.Join("..", "docparse", "testdata", "sample.pdf")
	data, err := os.ReadFile(src)
	require.NoError(t, err)

	dest := filepath.Join(dir, "sample.pdf")
	require.NoError(t, os.WriteFile(dest, data, 0644))

	result, err := b.IngestDocument(context.Background(), dest, "test", false)
	require.NoError(t, err)
	assert.Contains(t, result, "Parsed")
	assert.Contains(t, result, "pdf")
}

func TestIngestDocument_RejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{IngestDir: dir}
	b := New(nil, nil, cfg)

	// Create a .doc file — unsupported
	dest := filepath.Join(dir, "notes.doc")
	require.NoError(t, os.WriteFile(dest, []byte("hello"), 0644))

	_, err := b.IngestDocument(context.Background(), dest, "test", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestIngestDocument_RejectsPathOutsideIngestDir(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{IngestDir: dir}
	b := New(nil, nil, cfg)

	_, err := b.IngestDocument(context.Background(), "/etc/passwd", "test", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outside allowed")
}

func TestIngestDocument_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{IngestDir: dir}
	b := New(nil, nil, cfg)

	traversal := filepath.Join(dir, "..", "..", "etc", "passwd")
	_, err := b.IngestDocument(context.Background(), traversal, "test", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outside allowed")
}

func TestIngestDocument_RejectsEmptyPath(t *testing.T) {
	cfg := &config.Config{IngestDir: "/tmp"}
	b := New(nil, nil, cfg)

	_, err := b.IngestDocument(context.Background(), "", "test", false)
	assert.Error(t, err)
}

func TestIngestDocument_RejectsRelativePath(t *testing.T) {
	cfg := &config.Config{IngestDir: "/tmp"}
	b := New(nil, nil, cfg)

	_, err := b.IngestDocument(context.Background(), "relative/path.pdf", "test", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestIngestDocument_RejectsSymlinkOutsideDir(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{IngestDir: dir}
	b := New(nil, nil, cfg)

	// Create a symlink inside dir that points outside
	outsideFile := filepath.Join(t.TempDir(), "outside.pdf")
	require.NoError(t, os.WriteFile(outsideFile, []byte("fake"), 0644))

	symlink := filepath.Join(dir, "sneaky.pdf")
	require.NoError(t, os.Symlink(outsideFile, symlink))

	_, err := b.IngestDocument(context.Background(), symlink, "test", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outside allowed")
}

func TestIngestDocument_RejectsEmptyIngestDir(t *testing.T) {
	// When IngestDir is not configured, all ingestion should be rejected.
	cfg := &config.Config{IngestDir: ""}
	b := New(nil, nil, cfg)

	_, err := b.IngestDocument(context.Background(), "/tmp/test.pdf", "test", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestCheckFileSize_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	bigFile := filepath.Join(dir, "big.pdf")

	// Write a file that's 1024 bytes
	require.NoError(t, os.WriteFile(bigFile, make([]byte, 1024), 0644))

	// Limit of 512 bytes should reject it
	err := checkFileSize(bigFile, 512)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file too large")

	// Limit of 2048 bytes should accept it
	err = checkFileSize(bigFile, 2048)
	assert.NoError(t, err)
}

func TestCheckFileSize_FallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	smallFile := filepath.Join(dir, "small.pdf")
	require.NoError(t, os.WriteFile(smallFile, []byte("data"), 0644))

	// Zero maxBytes should use default (50 MB), so small file passes
	err := checkFileSize(smallFile, 0)
	assert.NoError(t, err)
}

func TestIngestDocument_RejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{IngestDir: dir, IngestMaxBytes: 100}
	b := New(nil, nil, cfg)

	// Create a file that exceeds 100 bytes
	dest := filepath.Join(dir, "large.pdf")
	require.NoError(t, os.WriteFile(dest, make([]byte, 200), 0644))

	_, err := b.IngestDocument(context.Background(), dest, "test", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file too large")
}

func TestDeepCaptureWithMeta_SignatureExists(t *testing.T) {
	// Verify DeepCaptureWithMeta exists with the correct signature and
	// handles the no-candidates case gracefully.
	cfg := &config.Config{}
	b := New(nil, nil, cfg)

	parsed := docparse.ParseResult{
		Text: "Test document content about architecture decisions.",
		Metadata: map[string]any{
			"filename": "test.pdf",
			"format":   "pdf",
		},
	}
	meta := map[string]any{"custom_key": "custom_value"}

	// Without LLM configured, ExtractThoughts returns nil, nil.
	// DeepCaptureWithMeta should handle this gracefully.
	result, err := b.DeepCaptureWithMeta(context.Background(), parsed, "test", meta)
	assert.NoError(t, err)
	assert.Contains(t, result, "0 thoughts captured")
}

func TestMergeMetadata_ImmutableMerge(t *testing.T) {
	base := map[string]any{"filename": "test.pdf", "format": "pdf"}
	overlay := map[string]any{"custom_key": "value", "source": "test"}

	merged := mergeMetadata(base, overlay)

	// Merged should contain all keys
	assert.Equal(t, "test.pdf", merged["filename"])
	assert.Equal(t, "pdf", merged["format"])
	assert.Equal(t, "value", merged["custom_key"])
	assert.Equal(t, "test", merged["source"])

	// Original maps should be unchanged (immutability)
	assert.Len(t, base, 2)
	assert.Len(t, overlay, 2)
}

func TestIngestDocument_LongTextChunked(t *testing.T) {
	// A text file longer than IngestChunkSize should produce multiple chunks
	// in the parse-only (autoCapture=false) summary.
	dir := t.TempDir()
	cfg := &config.Config{
		IngestDir:       dir,
		IngestChunkSize: 500,
	}
	b := New(nil, nil, cfg)

	// Create a text file with ~2500 chars.
	longText := ""
	for i := 0; i < 50; i++ {
		longText += "This is paragraph number. Some filler text here.\n\n"
	}
	dest := filepath.Join(dir, "long.txt")
	require.NoError(t, os.WriteFile(dest, []byte(longText), 0644))

	result, err := b.IngestDocument(context.Background(), dest, "test", false)
	require.NoError(t, err)
	assert.Contains(t, result, "chunks")
	assert.Contains(t, result, "Parsed")
}

func TestIngestDocument_ShortTextNotChunked(t *testing.T) {
	// A text file shorter than IngestChunkSize should NOT mention chunks.
	dir := t.TempDir()
	cfg := &config.Config{
		IngestDir:       dir,
		IngestChunkSize: 5000,
	}
	b := New(nil, nil, cfg)

	dest := filepath.Join(dir, "short.txt")
	require.NoError(t, os.WriteFile(dest, []byte("Short doc."), 0644))

	result, err := b.IngestDocument(context.Background(), dest, "test", false)
	require.NoError(t, err)
	assert.Contains(t, result, "Parsed")
	assert.NotContains(t, result, "chunks")
}

func TestIngestDocument_ChunkMetadataIncluded(t *testing.T) {
	// When autoCapture is true and text is long enough to chunk, each chunk's
	// metadata should include chunk_index, chunk_total, and source_file.
	// Without LLM configured, DeepCaptureWithMeta returns "0 thoughts captured"
	// for each chunk. We verify the summary mentions the chunk count.
	dir := t.TempDir()
	cfg := &config.Config{
		IngestDir:       dir,
		IngestChunkSize: 200,
	}
	b := New(nil, nil, cfg)

	longText := ""
	for i := 0; i < 30; i++ {
		longText += "Sentence with some content here.\n\n"
	}
	dest := filepath.Join(dir, "longdoc.txt")
	require.NoError(t, os.WriteFile(dest, []byte(longText), 0644))

	result, err := b.IngestDocument(context.Background(), dest, "test", true)
	require.NoError(t, err)
	// Summary should mention chunks (e.g. "5 chunks")
	assert.Contains(t, result, "chunks")
}

// TestDeepCaptureWithMeta_StoreFailureIsLoud asserts DeepCaptureWithMeta (used
// by the document-ingest path, distinct from DeepCapture used by the
// extract_thoughts tool) propagates a real, typed error when the atomic store
// of extracted candidates fails, rather than returning a success-shaped
// string. This pins the same OB-032 fail-loud contract DeepCapture already
// has, for the sibling entry point captureExtracted also serves.
func TestDeepCaptureWithMeta_StoreFailureIsLoud(t *testing.T) {
	b := New(nil, nil, &config.Config{})
	b.embedder = staticEmbedder{}
	b.extractFn = func(_ context.Context, _ string) ([]extract.Candidate, error) {
		return []extract.Candidate{
			{Content: "candidate one", ThoughtType: "note"},
			{Content: "candidate two", ThoughtType: "insight"},
		}, nil
	}
	b.bulkInsertFn = func(_ context.Context, _ []db.ThoughtInput) ([]string, error) {
		return nil, errors.New("injected store failure")
	}

	parsed := docparse.ParseResult{Text: "long document text", Metadata: map[string]any{}}
	result, err := b.DeepCaptureWithMeta(context.Background(), parsed, "test", map[string]any{})
	require.Error(t, err, "a store failure must surface as an error, not a success string")
	assert.Empty(t, result)
}

// TestDeepCaptureWithMeta_HappyPath asserts a successful extraction and store
// still returns the captured-count summary DeepCaptureWithMeta's callers
// (ingestChunks) rely on to report per-chunk success.
func TestDeepCaptureWithMeta_HappyPath(t *testing.T) {
	b := New(nil, nil, &config.Config{})
	b.embedder = staticEmbedder{}
	b.extractFn = func(_ context.Context, _ string) ([]extract.Candidate, error) {
		return []extract.Candidate{{Content: "candidate one", ThoughtType: "note"}}, nil
	}
	b.bulkInsertFn = func(_ context.Context, inputs []db.ThoughtInput) ([]string, error) {
		return []string{"00000001-0000-0000-0000-000000000000"}, nil
	}

	parsed := docparse.ParseResult{Text: "doc text", Metadata: map[string]any{}}
	result, err := b.DeepCaptureWithMeta(context.Background(), parsed, "test", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, result, "1 thoughts captured")
}

// TestIngestChunks_MidDocumentChunkFailureDoesNotAbortSiblings proves
// per-chunk atomicity is scoped to the CHUNK, not the document: when one
// chunk's underlying store call fails, that chunk contributes zero captures,
// but sibling chunks in the same document are processed independently and
// still succeed. This is the direct consequence of captureExtracted now being
// atomic-or-rollback per DeepCaptureWithMeta call: a failing chunk can no
// longer leave a partial set of its own candidates captured, but it also must
// not prevent unrelated sibling chunks from capturing. ingestChunks' own
// partial-success summary format ("N/M chunks captured") is pre-existing
// behavior and intentionally left unchanged here (OB-032 scope boundary).
func TestIngestChunks_MidDocumentChunkFailureDoesNotAbortSiblings(t *testing.T) {
	b := New(nil, nil, &config.Config{})
	b.embedder = staticEmbedder{}
	// One extracted candidate per chunk, equal to the chunk's own text, so the
	// test can tell which chunk produced which stored content.
	b.extractFn = func(_ context.Context, text string) ([]extract.Candidate, error) {
		return []extract.Candidate{{Content: text, ThoughtType: "note"}}, nil
	}

	var mu sync.Mutex
	var stored []string
	b.bulkInsertFn = func(_ context.Context, inputs []db.ThoughtInput) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		for _, in := range inputs {
			if strings.Contains(in.Content, "BOOM") {
				return nil, errors.New("injected mid-document chunk store failure")
			}
		}
		ids := make([]string, len(inputs))
		for i, in := range inputs {
			ids[i] = "id"
			stored = append(stored, in.Content)
		}
		return ids, nil
	}

	chunks := []chunker.Chunk{
		{Text: "chunk zero ok", Index: 0, Total: 3},
		{Text: "chunk one BOOM must fail to store", Index: 1, Total: 3},
		{Text: "chunk two ok", Index: 2, Total: 3},
	}

	result, err := b.ingestChunks(context.Background(), chunks, nil, "doc.txt", "test", map[string]any{})
	require.NoError(t, err, "one failed chunk must not fail the whole document when siblings succeed")
	assert.Contains(t, result, "2/3 chunks captured")

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, stored, 2, "exactly the two non-failing chunks' content must be persisted")
	for _, c := range stored {
		assert.NotContains(t, c, "BOOM", "the failed chunk's content must never be persisted")
	}
}

// TestCaptureExtracted_RejectsEmptyCandidateContent is the Wren LOW
// follow-up: captureExtracted's embed loop must refuse an empty-content
// candidate explicitly, not rely solely on extract.ExtractThoughts' own
// upstream filtering (extract.go) as an implicit guarantee. A synthetic
// empty candidate is injected past that filter via the extractFn seam, so
// this test exercises captureExtracted's own guard directly, proving a
// future extractFn swap cannot reopen the OB-049 empty-input bug at this
// embed site.
func TestCaptureExtracted_RejectsEmptyCandidateContent(t *testing.T) {
	b := New(nil, staticEmbedder{}, &config.Config{})
	bulkCalled := false
	b.bulkInsertFn = func(_ context.Context, _ []db.ThoughtInput) ([]string, error) {
		bulkCalled = true
		return nil, nil
	}
	b.extractFn = func(_ context.Context, _ string) ([]extract.Candidate, error) {
		return []extract.Candidate{
			{Content: "a real candidate", ThoughtType: "note"},
			{Content: "   ", ThoughtType: "note"}, // synthetic: bypasses extract.go's own filter
		}, nil
	}

	parsed := intent.ParsedIntent{Text: "source text", ThoughtType: "note"}
	result, err := b.DeepCapture(context.Background(), parsed, "test")

	require.Error(t, err, "an empty candidate must be refused, not silently embedded or dropped")
	assert.Empty(t, result)
	assert.ErrorIs(t, err, ErrEmptyText)
	assert.Contains(t, err.Error(), "candidate 1", "the error should identify which candidate was empty")
	assert.False(t, bulkCalled, "the store call must never run when a candidate fails the guard")
}

func TestTruncate_RuneSafe(t *testing.T) {
	// ASCII: truncate at 5 runes
	assert.Equal(t, "hello", truncate("hello world", 5))

	// Short string: returned as-is
	assert.Equal(t, "hi", truncate("hi", 10))

	// Multi-byte: truncate at 3 runes should not split a character
	assert.Equal(t, "日本語", truncate("日本語テスト", 3))

	// Emoji: each emoji is one rune
	assert.Equal(t, "🎉🎊", truncate("🎉🎊🎈", 2))
}
