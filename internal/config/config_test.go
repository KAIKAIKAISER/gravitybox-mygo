package config

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DebugPort != 9222 {
		t.Fatalf("expected default DebugPort 9222, got %d", cfg.DebugPort)
	}
	if !cfg.TranslationEnabled || !cfg.HudEnabled || !cfg.CodexThemeEnabled {
		t.Fatal("expected TranslationEnabled, HudEnabled, and CodexThemeEnabled to default true")
	}
	if cfg.CodexThemePreset != "auto" || cfg.TelemetryLayout != "compact" || cfg.TelemetryCacheMode != "session" {
		t.Fatalf("unexpected codex theme/telemetry defaults: %+v", cfg)
	}
	cfg.AccentColor = "emerald"
	cfg.ChatFontSize = 15
	vars := EffectiveCodexThemeVars(cfg)
	if vars["--primary"] != "#059669" || vars["--ag-chat-font-size"] != "15px" {
		t.Fatalf("unexpected resolved theme vars: %+v", vars)
	}
}
