package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routingTestCase struct {
	InputText       string `json:"input_text"`
	InputTextLen    int    `json:"input_text_len"`
	Threshold       int    `json:"threshold"`
	ExpectedPrimary bool   `json:"expected_needs_primary"`
	ActualPrimary   bool   `json:"actual_needs_primary"`
}

func routingTestdataPath(name string) string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "testdata", name)
}

// TestNeedsPrimaryModel pins the two-tier fast/primary selection heuristic that
// was migrated out of internal/llm and must be preserved EXACTLY post-migration.
func TestNeedsPrimaryModel(t *testing.T) {
	data, err := os.ReadFile(routingTestdataPath("llm_routing_cases.json"))
	require.NoError(t, err)

	var cases []routingTestCase
	require.NoError(t, json.Unmarshal(data, &cases))

	for _, tc := range cases {
		name := tc.InputText
		if len(name) > 50 {
			name = name[:50] + "..."
		}

		t.Run(name, func(t *testing.T) {
			text := tc.InputText
			if strings.HasPrefix(text, "[") && strings.Contains(text, "chars of") {
				text = strings.Repeat("x", tc.InputTextLen)
			}

			got := NeedsPrimaryModel(text, tc.Threshold)
			assert.Equal(t, tc.ActualPrimary, got, "routing mismatch for %q (len=%d, threshold=%d)", tc.InputText, tc.InputTextLen, tc.Threshold)
		})
	}
}
