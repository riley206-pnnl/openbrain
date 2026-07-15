package main

import (
	"strings"
	"testing"

	"github.com/windingriverholdings/openbrain/internal/config"
)

func TestRequireWebToken_EmptyToken_ReturnsError(t *testing.T) {
	cfg := &config.Config{WebWSToken: ""}

	err := requireWebToken(cfg)

	if err == nil {
		t.Fatal("requireWebToken() with empty WebWSToken = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "at least 32 characters long") {
		t.Fatalf("requireWebToken() error = %q, want it to name the 32-char minimum", err.Error())
	}
}

func TestRequireWebToken_TooShortToken_ReturnsError(t *testing.T) {
	cfg := &config.Config{WebWSToken: "abc"}

	err := requireWebToken(cfg)

	if err == nil {
		t.Fatal("requireWebToken() with a 3-char WebWSToken = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "at least 32 characters long") {
		t.Fatalf("requireWebToken() error = %q, want it to name the 32-char minimum", err.Error())
	}
}

func TestRequireWebToken_SetToken_ReturnsNil(t *testing.T) {
	cfg := &config.Config{WebWSToken: "a-sufficiently-long-web-auth-token"}

	err := requireWebToken(cfg)

	if err != nil {
		t.Fatalf("requireWebToken() with set WebWSToken = %v, want nil", err)
	}
}
