package mcptools

import (
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
)

// toolText creates a successful MCP tool result with text content.
func toolText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(text)},
	}
}

// toolError creates an error MCP tool result with text content.
func toolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(text)},
		IsError: true,
	}
}

// requireString extracts a required string argument by key. On success it
// returns the value with ok=true. On failure (key absent, or present with
// the wrong type) it logs the rejection server-side (tool and key only, no
// payload) and returns a caller-safe error result naming the key, so the
// caller never has to guess which argument was missing.
func requireString(tool string, request mcp.CallToolRequest, key string) (string, *mcp.CallToolResult, bool) {
	val, err := request.RequireString(key)
	if err != nil {
		slog.Warn("rejected tool call: missing or invalid required argument", "tool", tool, "key", key, "error", err)
		return "", toolError(err.Error()), false
	}
	return val, nil, true
}

// stringArg extracts a string from the args map, returning fallback if absent.
func stringArg(args map[string]any, key, fallback string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// boolArg extracts a bool from the args map, returning fallback if absent.
func boolArg(args map[string]any, key string, fallback bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return fallback
}

// stringListArg extracts a string slice from an []any value in args.
func stringListArg(args map[string]any, key string) []string {
	arr, ok := args[key].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// stringListFromObj extracts a string slice from a map value.
func stringListFromObj(obj map[string]any, key string) []string {
	arr, ok := obj[key].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
