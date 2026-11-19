package config

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DebugPort != 9222 {
		t.Fatalf("expected default DebugPort 9222, got %d", cfg.DebugPort)
	}
	if !cfg.TranslationEnabled || !cfg.HudEnabled {
		t.Fatal("expected TranslationEnabled and HudEnabled to default true")
	}
}
