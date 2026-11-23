package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config stores persistent GravityBox user preferences in %APPDATA%\GravityBox\config.json.
type Config struct {
	AppLanguage          string            `json:"appLanguage"`          // "system", "zh-Hans", "en"
	AppTheme             string            `json:"appTheme"`             // "system", "light", "dark"
	TranslationEnabled   bool              `json:"translationEnabled"`   // IDE runtime / ASAR translation toggle
	HudEnabled           bool              `json:"hudEnabled"`           // Conversation HUD toggle
	CodexThemeEnabled    bool              `json:"codexThemeEnabled"`    // Inject Codex day/night theme into Antigravity
	CodexThemePreset     string            `json:"codexThemePreset"`     // "auto", "codex-pure-white", "codex-dark"
	TelemetryLayout      string            `json:"telemetryLayout"`      // "compact", "detailed"
	TelemetryCacheMode   string            `json:"telemetryCacheMode"`   // "session", "step"
	ManualContextLimit   int               `json:"manualContextLimit"`   // 0 = auto detect from DB, else token limit
	AccentColor          string            `json:"accentColor"`          // "blue", "emerald", "indigo", "amber"
	ChatFontSize         int               `json:"chatFontSize"`         // 0 = default, or 14/15/16 px
	ThemeVars            map[string]string `json:"themeVars,omitempty"`  // Optional CSS var overrides for Codex theme
	StartInTray          bool              `json:"startInTray"`
	LaunchAtLogin        bool              `json:"launchAtLogin"`
	FollowAntigravity    bool              `json:"followAntigravity"`
	QuotaRefreshInterval int               `json:"quotaRefreshInterval"` // seconds; 0 = manual
	AutoPricingSync      bool              `json:"autoPricingSync"`
	AutoCheckUpdates     bool              `json:"autoCheckUpdates"`
	AppPath              string            `json:"appPath,omitempty"`
	DebugPort            int               `json:"debugPort"`
}

// DefaultConfig returns sensible defaults aligned with GravityBox.
func DefaultConfig() Config {
	return Config{
		AppLanguage:          "system",
		AppTheme:             "system",
		TranslationEnabled:   true,
		HudEnabled:           true,
		CodexThemeEnabled:    true,
		CodexThemePreset:     "auto",
		TelemetryLayout:      "compact",
		TelemetryCacheMode:   "session",
		ManualContextLimit:   0,
		AccentColor:          "blue",
		ChatFontSize:         0,
		StartInTray:          false,
		LaunchAtLogin:        false,
		FollowAntigravity:    false,
		QuotaRefreshInterval: 0,
		AutoPricingSync:      false,
		AutoCheckUpdates:     true,
		DebugPort:            9222,
	}
}

// StateDir returns %APPDATA%\GravityBox on Windows.
func StateDir() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "GravityBox")
		}
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "GravityBox")
	}
	return filepath.Join(home, ".config", "GravityBox")
}

// CacheDir returns %LOCALAPPDATA%\GravityBox\Cache on Windows.
func CacheDir() string {
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "GravityBox", "Cache")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "GravityBox")
}

// TrashDir returns the non-destructive maintenance trash directory.
func TrashDir() string {
	return filepath.Join(CacheDir(), "maintenance-trash")
}

// AntigravityUserDataDir returns %APPDATA%\Antigravity on Windows.
func AntigravityUserDataDir() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Antigravity")
		}
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Antigravity")
	}
	return filepath.Join(home, ".config", "Antigravity")
}

// AntigravityLogFile returns the language_server.log path for retry log inspection.
func AntigravityLogFile() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Logs", "Antigravity", "language_server.log")
	}
	return filepath.Join(AntigravityUserDataDir(), "logs", "language_server.log")
}

// EffectiveCodexThemeVars returns merged CSS variable overrides for the injected Codex theme.
func EffectiveCodexThemeVars(cfg Config) map[string]string {
	out := make(map[string]string)
	switch cfg.AccentColor {
	case "emerald":
		out["--primary"] = "#059669"
		out["--ag-send-btn-bg"] = "#059669"
		out["--ag-send-btn-hover-bg"] = "#047857"
		out["--ag-send-btn-border"] = "#059669"
		out["--ag-send-btn-shadow"] = "0 2px 6px rgba(5, 150, 105, 0.28)"
		out["--editor-selection-background"] = "rgba(5, 150, 105, 0.22)"
	case "indigo":
		out["--primary"] = "#6366F1"
		out["--ag-send-btn-bg"] = "#6366F1"
		out["--ag-send-btn-hover-bg"] = "#4F46E5"
		out["--ag-send-btn-border"] = "#6366F1"
		out["--ag-send-btn-shadow"] = "0 2px 6px rgba(99, 102, 241, 0.28)"
		out["--editor-selection-background"] = "rgba(99, 102, 241, 0.22)"
	case "amber":
		out["--primary"] = "#D97706"
		out["--ag-send-btn-bg"] = "#D97706"
		out["--ag-send-btn-hover-bg"] = "#B45309"
		out["--ag-send-btn-border"] = "#D97706"
		out["--ag-send-btn-shadow"] = "0 2px 6px rgba(217, 119, 6, 0.28)"
		out["--editor-selection-background"] = "rgba(217, 119, 6, 0.22)"
	}
	if cfg.ChatFontSize >= 12 && cfg.ChatFontSize <= 22 {
		out["--ag-chat-font-size"] = string(rune('0'+cfg.ChatFontSize/10)) + string(rune('0'+cfg.ChatFontSize%10)) + "px"
	}
	for k, v := range cfg.ThemeVars {
		if strings.HasPrefix(k, "--") && strings.TrimSpace(v) != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// GeminiDir returns ~/.gemini.
func GeminiDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini")
}

// DetectAntigravityInstallDir locates the Antigravity installation directory on Windows.
func DetectAntigravityInstallDir(override string) string {
	candidates := make([]string, 0, 5)
	if strings.TrimSpace(override) != "" {
		candidates = append(candidates, override)
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "Programs", "Antigravity"),
			filepath.Join(localAppData, "Antigravity"),
		)
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		candidates = append(candidates, filepath.Join(pf, "Antigravity"))
	}
	if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
		candidates = append(candidates, filepath.Join(pf86, "Antigravity"))
	}

	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		exe := filepath.Join(dir, "Antigravity.exe")
		asar := filepath.Join(dir, "resources", "app.asar")
		if fileExists(exe) || fileExists(asar) {
			return dir
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Load reads config.json from StateDir, falling back to DefaultConfig on missing/invalid file.
func Load() Config {
	cfg := DefaultConfig()
	path := filepath.Join(StateDir(), "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return cfg
	}
	if v, ok := parsed["appLanguage"].(string); ok && v != "" {
		cfg.AppLanguage = v
	}
	if v, ok := parsed["appTheme"].(string); ok && (v == "system" || v == "light" || v == "dark") {
		cfg.AppTheme = v
	}
	if v, ok := parsed["translationEnabled"].(bool); ok {
		cfg.TranslationEnabled = v
	}
	if v, ok := parsed["hudEnabled"].(bool); ok {
		cfg.HudEnabled = v
	}
	if v, ok := parsed["codexThemeEnabled"].(bool); ok {
		cfg.CodexThemeEnabled = v
	}
	if v, ok := parsed["codexThemePreset"].(string); ok && (v == "auto" || v == "codex-pure-white" || v == "codex-dark") {
		cfg.CodexThemePreset = v
	}
	if v, ok := parsed["telemetryLayout"].(string); ok && (v == "compact" || v == "detailed") {
		cfg.TelemetryLayout = v
	}
	if v, ok := parsed["telemetryCacheMode"].(string); ok && (v == "session" || v == "step") {
		cfg.TelemetryCacheMode = v
	}
	if v, ok := parsed["manualContextLimit"].(float64); ok && v >= 0 {
		cfg.ManualContextLimit = int(v)
	}
	if v, ok := parsed["accentColor"].(string); ok && (v == "blue" || v == "emerald" || v == "indigo" || v == "amber") {
		cfg.AccentColor = v
	}
	if v, ok := parsed["chatFontSize"].(float64); ok && (int(v) == 0 || (int(v) >= 12 && int(v) <= 22)) {
		cfg.ChatFontSize = int(v)
	}
	if rawVars, ok := parsed["themeVars"].(map[string]any); ok && len(rawVars) > 0 {
		vars := make(map[string]string, len(rawVars))
		for k, val := range rawVars {
			if s, ok := val.(string); ok && strings.HasPrefix(k, "--") && strings.TrimSpace(s) != "" {
				vars[k] = strings.TrimSpace(s)
			}
		}
		if len(vars) > 0 {
			cfg.ThemeVars = vars
		}
	}
	if v, ok := parsed["startInTray"].(bool); ok {
		cfg.StartInTray = v
	}
	if v, ok := parsed["launchAtLogin"].(bool); ok {
		cfg.LaunchAtLogin = v
	}
	if v, ok := parsed["followAntigravity"].(bool); ok {
		cfg.FollowAntigravity = v
	}
	if v, ok := parsed["quotaRefreshInterval"].(float64); ok && v >= 0 {
		cfg.QuotaRefreshInterval = int(v)
	}
	if v, ok := parsed["autoPricingSync"].(bool); ok {
		cfg.AutoPricingSync = v
	}
	if v, ok := parsed["autoCheckUpdates"].(bool); ok {
		cfg.AutoCheckUpdates = v
	}
	if v, ok := parsed["appPath"].(string); ok {
		cfg.AppPath = v
	}
	if v, ok := parsed["debugPort"].(float64); ok && v > 0 && v < 65536 {
		cfg.DebugPort = int(v)
	}
	return cfg
}

// Save atomically writes config.json in StateDir.
func Save(cfg Config) error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, "config.json.tmp")
	target := filepath.Join(dir, "config.json")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}
