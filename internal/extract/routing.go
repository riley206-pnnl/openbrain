package extract

import (
	"fmt"
	"log/slog"
	"regexp"
	"sync"

	wrsllm "github.com/windingriverholdings/wrs-llm"

	"github.com/windingriverholdings/openbrain/internal/config"
)

// correctionPatterns matches phrasing that signals a fact update/correction.
// Such text is routed to the primary (accurate) model regardless of length.
var correctionPatterns = regexp.MustCompile(
	`(?i)(actually[,:\s]|correction[,:\s]|update[,:\s]|no longer[,:\s]` +
		`|now instead[,:\s]|changed?[,:\s]|not \d+%.*but \d+%` +
		`|was wrong|I was mistaken|turns out)`)

// NeedsPrimaryModel returns true if text should use the primary (accurate)
// model rather than the fast model: long text or correction-heavy text.
//
// This heuristic is openbrain's two-tier policy. wrs-llm v0.1.0's router is a
// TaskKind -> single-model map with hard guards and has no length-threshold
// fast/primary tier, so the selection is kept here at the call site (see the
// v0.2.0 enhancement note in the migration PR).
func NeedsPrimaryModel(text string, threshold int) bool {
	if len(text) > threshold {
		return true
	}
	return correctionPatterns.MatchString(text)
}

// tierSelector holds the resolved fast/primary providers and the threshold used
// to choose between them. A nil primary means extraction is disabled (the
// "none" provider mode).
type tierSelector struct {
	primary   wrsllm.Provider
	fast      wrsllm.Provider
	hasFast   bool
	threshold int
}

// selectProvider returns the provider to use for the given text, applying the
// two-tier heuristic. Returns nil (no error) when extraction is disabled.
func (s *tierSelector) selectProvider(text string) (wrsllm.Provider, error) {
	if s.primary == nil {
		return nil, nil
	}
	if s.hasFast && s.fast != nil && text != "" && !NeedsPrimaryModel(text, s.threshold) {
		return s.fast, nil
	}
	return s.primary, nil
}

// --- config-driven cached selector + reload reset ---

var (
	cachedSelector *tierSelector
	selectorMu     sync.Mutex
)

// ResetProviders clears the cached selector so the next extraction rebuilds it
// from config. Called on .env reload (see brain.reload).
func ResetProviders() {
	selectorMu.Lock()
	defer selectorMu.Unlock()
	cachedSelector = nil
	slog.Info("llm providers reset")
}

// getSelector returns the active selector. Tests may override it via
// setSelectorForTest; otherwise it lazily builds a config-driven one and caches
// it (mirroring the prior internal/llm global-cache behavior).
func getSelector() (*tierSelector, error) {
	selectorMu.Lock()
	defer selectorMu.Unlock()

	if testSelector != nil {
		return testSelector, nil
	}
	if cachedSelector != nil {
		return cachedSelector, nil
	}

	sel, err := buildSelector(config.Get())
	if err != nil {
		return nil, err
	}
	cachedSelector = sel
	return sel, nil
}

// buildSelector maps openbrain config into wrs-llm providers, preserving the
// two-tier fast/primary setup exactly:
//   - provider type from OPENBRAIN_EXTRACT_PROVIDER (ollama / claude / none)
//   - baseURL from OPENBRAIN_OLLAMA_BASE_URL, models from EXTRACT_MODEL[_FAST]
//   - a fast tier exists only when EXTRACT_MODEL_FAST is set and differs from
//     EXTRACT_MODEL (identical to the prior hasFast condition)
func buildSelector(cfg *config.Config) (*tierSelector, error) {
	if cfg.ExtractProvider == "none" {
		return &tierSelector{threshold: cfg.ExtractFastThreshold}, nil
	}

	primary, err := buildProvider(cfg.ExtractProvider, cfg.ExtractModel, cfg)
	if err != nil {
		return nil, err
	}

	hasFast := cfg.ExtractModelFast != "" && cfg.ExtractModelFast != cfg.ExtractModel
	var fast wrsllm.Provider
	if hasFast {
		fast, err = buildProvider(cfg.ExtractProvider, cfg.ExtractModelFast, cfg)
		if err != nil {
			return nil, err
		}
		slog.Info("llm providers ready",
			"provider", cfg.ExtractProvider,
			"primary", cfg.ExtractModel,
			"fast", cfg.ExtractModelFast)
	} else {
		slog.Info("llm provider ready",
			"provider", cfg.ExtractProvider,
			"model", cfg.ExtractModel)
	}

	return &tierSelector{
		primary:   primary,
		fast:      fast,
		hasFast:   hasFast,
		threshold: cfg.ExtractFastThreshold,
	}, nil
}

// buildProvider constructs a wrs-llm provider of the configured type. A zero
// timeout lets wrs-llm apply its defaults (120s Ollama / 60s Claude), which
// match openbrain's prior per-provider timeouts exactly.
func buildProvider(providerType, model string, cfg *config.Config) (wrsllm.Provider, error) {
	switch providerType {
	case "ollama":
		return wrsllm.NewOllamaProvider(cfg.OllamaBaseURL, model, 0), nil
	case "claude":
		if cfg.AnthropicAPIKey == "" {
			return nil, fmt.Errorf("OPENBRAIN_ANTHROPIC_API_KEY required for claude provider")
		}
		return wrsllm.NewClaudeProvider(cfg.AnthropicAPIKey, model, 0), nil
	default:
		return nil, fmt.Errorf("unknown extract_provider: %s", providerType)
	}
}

// testSelector, when non-nil, overrides the config-driven selector. Set only by
// setSelectorForTest in tests.
var testSelector *tierSelector

// setSelectorForTest installs a fixed selector and returns a restore func.
func setSelectorForTest(s *tierSelector) func() {
	selectorMu.Lock()
	prev := testSelector
	testSelector = s
	selectorMu.Unlock()
	return func() {
		selectorMu.Lock()
		testSelector = prev
		selectorMu.Unlock()
	}
}
