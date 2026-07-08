package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/intent"
	"github.com/windingriverholdings/openbrain/internal/pathsec"
)

// mcpCapture routes capture through brain.Capture.
func mcpCapture(b *brain.Brain) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, errResult, ok := requireString("capture_thought", request, "content")
		if !ok {
			return errResult, nil
		}
		args := request.GetArguments()
		thoughtType, _ := args["thought_type"].(string)
		if thoughtType == "" {
			thoughtType = intent.InferType(content)
		}
		source := stringArg(args, "source", "claude")
		tags := stringListArg(args, "tags")

		parsed := intent.ParsedIntent{
			Intent:      intent.Capture,
			Text:        content,
			ThoughtType: thoughtType,
			Tags:        tags,
		}
		result, err := b.Capture(ctx, parsed, source)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolText(result), nil
	}
}

// mcpSearch routes search through brain.Search with formatted output.
func mcpSearch(b *brain.Brain) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, errResult, ok := requireString("search_thoughts", request, "query")
		if !ok {
			return errResult, nil
		}
		args := request.GetArguments()

		mode := stringArg(args, "mode", "hybrid")
		if !validSearchModes[mode] {
			return toolError(fmt.Sprintf("invalid mode %q: must be one of hybrid, vector, keyword", mode)), nil
		}

		thoughtType := stringArg(args, "thought_type", "")
		if thoughtType != "" && !validThoughtTypes[thoughtType] {
			return toolError(fmt.Sprintf("invalid thought_type %q: must be one of decision, insight, person, meeting, idea, note, memory", thoughtType)), nil
		}

		opts := brain.SearchOpts{
			Mode:           mode,
			ThoughtType:    thoughtType,
			Tags:           stringListArg(args, "tags"),
			IncludeHistory: boolArg(args, "include_history", false),
		}

		results, err := b.Search(ctx, query, opts)
		if err != nil {
			return toolError(err.Error()), nil
		}

		if len(results) == 0 {
			return toolText("No matching thoughts found."), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Found %d thought(s) related to: %q\n\n", len(results), query)
		for i, t := range results {
			score := ""
			if t.Score != nil {
				score = fmt.Sprintf(" (score: %.4f)", *t.Score)
			}
			fmt.Fprintf(&sb, "%d. [%s]%s — %s\n   %s\n",
				i+1, t.ThoughtType, score, t.CreatedAt.Format("2006-01-02"), t.Content)
			if len(t.Tags) > 0 {
				fmt.Fprintf(&sb, "   Tags: %s\n", strings.Join(t.Tags, ", "))
			}
			sb.WriteString("\n")
		}

		return toolText(sb.String()), nil
	}
}

// mcpDispatch routes any intent through brain.Dispatch.
func mcpDispatch(b *brain.Brain, i intent.Intent) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		parsed := intent.ParsedIntent{Intent: i, Text: string(i), ThoughtType: "note"}
		result, err := b.Dispatch(ctx, parsed, "claude")
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolText(result), nil
	}
}

// mcpBulkImport imports multiple thoughts as one atomic batch through
// brain.BulkImport: either every thought is saved or none is. A malformed item
// or a store failure aborts the whole batch and returns an error result, never
// a success-shaped summary that hides a partial write.
func mcpBulkImport(b *brain.Brain) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		// "thoughts" is a required array of objects, not a string, so it is
		// not a candidate for requireString; mcp-go v0.30.0 has no typed
		// RequireArray accessor for heterogeneous []any. The presence and
		// type check below already names the key on rejection.
		raw, ok := args["thoughts"].([]any)
		if !ok {
			slog.Warn("rejected tool call: missing or invalid required argument", "tool", "bulk_import", "key", "thoughts")
			return toolError("thoughts must be an array"), nil
		}
		source := stringArg(args, "source", "import")

		items := make([]brain.BulkItem, 0, len(raw))
		for i, t := range raw {
			obj, ok := t.(map[string]any)
			if !ok {
				return toolError(fmt.Sprintf("thought %d is not an object; nothing was saved", i)), nil
			}
			content, _ := obj["content"].(string)
			thoughtType, _ := obj["thought_type"].(string)
			items = append(items, brain.BulkItem{
				Content:     content,
				ThoughtType: thoughtType,
				Tags:        stringListFromObj(obj, "tags"),
			})
		}

		ids, err := b.BulkImport(ctx, items, source)
		if err != nil {
			slog.Error("bulk_import tool: store failed", "error", err)
			return toolError(fmt.Sprintf("bulk import aborted, nothing was saved: %s", sanitizeStoreError(err))), nil
		}
		return toolText(fmt.Sprintf("Imported %d thoughts atomically", len(ids))), nil
	}
}

// mcpExtract routes through brain.DeepCapture.
func mcpExtract(b *brain.Brain) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text, errResult, ok := requireString("extract_thoughts", request, "text")
		if !ok {
			return errResult, nil
		}
		args := request.GetArguments()
		autoCapture := true
		if ac, ok := args["auto_capture"].(bool); ok {
			autoCapture = ac
		}
		source := stringArg(args, "source", "claude")

		if !autoCapture {
			candidates, err := extractOnly(ctx, text)
			if err != nil {
				return toolError(err.Error()), nil
			}
			data, err := json.MarshalIndent(candidates, "", "  ")
			if err != nil {
				return toolError(fmt.Sprintf("failed to format results: %v", err)), nil
			}
			return toolText(fmt.Sprintf("Extracted %d candidates:\n%s", len(candidates), data)), nil
		}

		parsed := intent.ParsedIntent{Intent: intent.Extract, Text: text, ThoughtType: "note"}
		result, err := b.DeepCapture(ctx, parsed, source)
		if err != nil {
			slog.Error("extract_thoughts tool: auto-capture failed", "error", err)
			return toolError(sanitizeStoreError(err)), nil
		}
		return toolText(result), nil
	}
}

// mcpSupersede routes through brain.Supersede with optional query/ID overrides.
func mcpSupersede(b *brain.Brain) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, errResult, ok := requireString("supersede_thought", request, "content")
		if !ok {
			return errResult, nil
		}
		args := request.GetArguments()
		thoughtType := stringArg(args, "thought_type", "")
		if thoughtType == "" {
			thoughtType = intent.InferType(content)
		}
		source := stringArg(args, "source", "claude")
		tags := stringListArg(args, "tags")

		parsed := intent.ParsedIntent{
			Intent:      intent.Supersede,
			Text:        content,
			ThoughtType: thoughtType,
			Tags:        tags,
		}

		if q := stringArg(args, "supersedes_query", ""); q != "" {
			parsed.SupersedeQuery = &q
		}
		if id := stringArg(args, "old_thought_id", ""); id != "" {
			parsed.OldThoughtID = &id
		}

		result, err := b.Supersede(ctx, parsed, source)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolText(result), nil
	}
}

// mcpIngestDocument handles the ingest_document MCP tool.
func mcpIngestDocument(b *brain.Brain, cfg *config.Config) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath, errResult, ok := requireString("ingest_document", request, "file_path")
		if !ok {
			return errResult, nil
		}
		args := request.GetArguments()
		source := stringArg(args, "source", "claude")
		autoCapture := boolArg(args, "auto_capture", true)

		if len([]rune(source)) > sourceMaxLen {
			return toolError("source parameter exceeds 255 character limit"), nil
		}

		if err := pathsec.ValidateIngestPath(filePath, cfg.IngestDir); err != nil {
			return toolError(sanitizeIngestError(err.Error())), nil
		}

		result, err := b.IngestDocument(ctx, filePath, source, autoCapture)
		if err != nil {
			return toolError(sanitizeIngestError(err.Error())), nil
		}

		return toolText(result), nil
	}
}

// extractOnly calls the extract package without capturing.
func extractOnly(ctx context.Context, text string) ([]any, error) {
	candidates, err := extractFunc(ctx, text)
	if err != nil {
		return nil, err
	}
	result := make([]any, len(candidates))
	for i, c := range candidates {
		result[i] = c
	}
	return result, nil
}

// sanitizeStoreError reduces an internal write-path error to a caller-safe
// message, scrubbing raw storage-driver detail (SQLSTATE codes, column and
// enum names) the same way sanitizeIngestError scrubs filesystem detail. Known
// sentinel errors defined by this codebase's own domain (batch/content-size
// caps, empty batch, empty content, empty embedding) carry no internal detail,
// so their own message is caller-safe and is surfaced as-is; anything else
// (a raw store or embed failure) collapses to a generic message. Callers log
// the raw error server-side via slog.Error before calling this, so the detail
// is never lost, only kept out of the tool response.
func sanitizeStoreError(err error) string {
	switch {
	case errors.Is(err, db.ErrEmptyBatch):
		return "no thoughts to import: the batch was empty"
	case errors.Is(err, db.ErrEmptyEmbedding):
		return "one or more thoughts could not be embedded"
	case errors.Is(err, brain.ErrEmptyContent):
		return "one or more thoughts has empty content"
	case errors.Is(err, brain.ErrBatchTooLarge):
		return err.Error()
	case errors.Is(err, brain.ErrContentTooLong):
		return err.Error()
	default:
		return "operation failed: see server log for detail"
	}
}

// sanitizeIngestError removes internal path information from error messages.
func sanitizeIngestError(errMsg string) string {
	if strings.Contains(errMsg, "no such file") {
		return "file not found"
	}
	if strings.Contains(errMsg, "permission denied") {
		return "file not accessible"
	}
	if strings.Contains(errMsg, "outside allowed") {
		return "file path not allowed"
	}
	if strings.Contains(errMsg, "not configured") {
		return "document ingestion is not configured"
	}
	if strings.Contains(errMsg, "unsupported") {
		return errMsg
	}
	if strings.Contains(errMsg, "file too large") {
		return "file too large (limit: configurable via OPENBRAIN_INGEST_MAX_BYTES)"
	}
	return "ingestion failed"
}
