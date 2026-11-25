package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DebugPort != 9222 {
		t.Fatalf("expected default DebugPort 9222, got %d", cfg.DebugPort)
	}
	if !cfg.TranslationEnabled || !cfg.HudEnabled || !cfg.CodexThemeEnabled || !cfg.CodexFontEnabled {
		t.Fatal("expected TranslationEnabled, HudEnabled, CodexThemeEnabled, and CodexFontEnabled to default true")
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
	cfg.CodexFontEnabled = false
	varsNoFont := EffectiveCodexThemeVars(cfg)
	if _, ok := varsNoFont["--ag-chat-font-size"]; ok {
		t.Fatalf("expected --ag-chat-font-size to be suppressed when CodexFontEnabled=false, got %+v", varsNoFont)
	}
}

func TestSaveAndLoadPreservesCodexFontEnabledFalse(t *testing.T) {
	tmp := t.TempDir()
	appData := filepath.Join(tmp, "roaming")
	t.Setenv("APPDATA", appData)
	t.Setenv("XDG_CONFIG_HOME", appData)
	t.Setenv("HOME", tmp)

	cfg := DefaultConfig()
	cfg.CodexFontEnabled = false
	cfg.ChatFontSize = 16
	if err := Save(cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(StateDir(), "config.json")); err != nil {
		t.Fatalf("expected config.json to exist: %v", err)
	}
	loaded := Load()
	if loaded.CodexFontEnabled {
		t.Fatalf("expected CodexFontEnabled=false after Save+Load, got true")
	}
	if loaded.ChatFontSize != 16 {
		t.Fatalf("expected ChatFontSize=16 after Save+Load, got %d", loaded.ChatFontSize)
	}
}
