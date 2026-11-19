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
	AppLanguage        string `json:"appLanguage"`        // "system", "zh-Hans", "en"
	TranslationEnabled bool   `json:"translationEnabled"` // IDE runtime / ASAR translation toggle
	HudEnabled         bool   `json:"hudEnabled"`         // Conversation HUD toggle
	StartInTray        bool   `json:"startInTray"`
	AppPath            string `json:"appPath,omitempty"` // Optional override for Antigravity install root
	DebugPort          int    `json:"debugPort"`         // Default 9222
}

// DefaultConfig returns sensible defaults aligned with GravityBox v1.2.0.
func DefaultConfig() Config {
	return Config{
		AppLanguage:        "system",
		TranslationEnabled: true,
		HudEnabled:         true,
		StartInTray:        false,
		DebugPort:          9222,
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
	if v, ok := parsed["translationEnabled"].(bool); ok {
		cfg.TranslationEnabled = v
	}
	if v, ok := parsed["hudEnabled"].(bool); ok {
		cfg.HudEnabled = v
	}
	if v, ok := parsed["startInTray"].(bool); ok {
		cfg.StartInTray = v
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
