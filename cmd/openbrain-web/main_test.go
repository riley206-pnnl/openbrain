package main

import (
	"testing"

	"github.com/windingriverholdings/openbrain/internal/config"
)

func TestRequireWebToken_EmptyToken_ReturnsError(t *testing.T) {
	cfg := &config.Config{WebWSToken: ""}

	err := requireWebToken(cfg)

	if err == nil {
		t.Fatal("requireWebToken() with empty WebWSToken = nil error, want non-nil")
	}
}

func TestRequireWebToken_SetToken_ReturnsNil(t *testing.T) {
	cfg := &config.Config{WebWSToken: "a-sufficiently-long-web-auth-token"}

	err := requireWebToken(cfg)

	if err != nil {
		t.Fatalf("requireWebToken() with set WebWSToken = %v, want nil", err)
	}
}
