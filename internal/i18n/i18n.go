package i18n

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"gravitybox-mygo/resources"
)

type packFile struct {
	App map[string]string `json:"app"`
}

var (
	once      sync.Once
	zhStrings map[string]string
	enStrings map[string]string
)

func loadPacks() {
	once.Do(func() {
		zhStrings = readPack("assets/i18n/zh-Hans.json")
		enStrings = readPack("assets/i18n/en.json")
	})
}

func readPack(rel string) map[string]string {
	raw, err := resources.ReadFile(rel)
	if err != nil {
		return map[string]string{}
	}
	var p packFile
	if err := json.Unmarshal(raw, &p); err != nil || p.App == nil {
		return map[string]string{}
	}
	return p.App
}

// ResolveLocale resolves "system" / locale tag into "zh-Hans" or "en".
func ResolveLocale(configured string) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "zh", "zh-cn", "zh-hans", "zh_cn", "zh_hans":
		return "zh-Hans"
	case "en", "en-us", "en-gb":
		return "en"
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.ToLower(os.Getenv(env)); strings.HasPrefix(v, "zh") {
			return "zh-Hans"
		}
	}
	return "zh-Hans"
}

// T looks up a localized string from embedded GravityBox language packs with fallback.
func T(locale, key, fallback string) string {
	loadPacks()
	resolved := ResolveLocale(locale)
	if resolved == "en" {
		if v, ok := enStrings[key]; ok && v != "" {
			return v
		}
		if v, ok := zhStrings[key]; ok && v != "" {
			return v
		}
		return fallback
	}
	if v, ok := zhStrings[key]; ok && v != "" {
		return v
	}
	if v, ok := enStrings[key]; ok && v != "" {
		return v
	}
	return fallback
}
