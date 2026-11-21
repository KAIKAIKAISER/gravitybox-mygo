package hud

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gravitybox-mygo/resources"
)

const DefaultContextCapacityTokens = 200_000

// ModelCapacitySpec describes a resolved model's display name, ID, and context capacity.
type ModelCapacitySpec struct {
	ModelID         string
	DisplayName     string
	ShortLabel      string
	CompactLabel    string
	CapacityTokens  int64
	CapacityCompact string
	IsFallback      bool
}

type pricingProvenance struct {
	ValidUntil string `json:"validUntil"`
}

type pricingInterval struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	Cached     float64 `json:"cached"`
	CacheWrite float64 `json:"cache_write"`
	ValidFrom  *string `json:"validFrom"`
	ValidUntil *string `json:"validUntil"`
}

// PriceRule holds per-1M token pricing (plus optional date intervals).
type PriceRule struct {
	Model      string            `json:"model"`
	Input      float64           `json:"input"`
	Output     float64           `json:"output"`
	Cached     float64           `json:"cached"`
	CacheRead  float64           `json:"cache_read"`
	CacheWrite float64           `json:"cache_write"`
	Intervals  []pricingInterval `json:"intervals"`
}

type modelCatalogFile struct {
	Names      map[string]string    `json:"names"`
	Capacities map[string]int64     `json:"capacities"`
	Prices     map[string]PriceRule `json:"prices"`
	Aliases    map[string]string    `json:"aliases"`
}

type pricingFeedFile struct {
	Models     map[string]json.RawMessage `json:"models"`
	Prices     map[string]json.RawMessage `json:"prices"`
	Aliases    map[string]string          `json:"aliases"`
	Names      map[string]string          `json:"names"`
	Capacities map[string]int64           `json:"capacities"`
}

type familyRule struct {
	re       *regexp.Regexp
	capacity int64
	compact  string
}

var (
	catalogOnce sync.Once
	catNames    map[string]string
	catCaps     map[string]int64
	catPrices   map[string]PriceRule
	catAliases  map[string]string

	familyRules = []familyRule{
		{re: regexp.MustCompile(`(?i)^gemini-(?:1\.5|2\.0|2\.5|3(?:\.\d+)?)-pro|^gemini-pro`), capacity: 200_000, compact: "200K"},
		{re: regexp.MustCompile(`(?i)^gemini-(?:1\.5|2\.0|2\.5|3(?:\.\d+)?)-flash`), capacity: 200_000, compact: "200K"},
		{re: regexp.MustCompile(`(?i)^gemini-[4-9](?:\.\d+)?-[a-z0-9]+`), capacity: 200_000, compact: "200K"},
		{re: regexp.MustCompile(`(?i)^claude-(?:3|4|5)|^claude-(?:sonnet|opus|haiku)`), capacity: 200_000, compact: "200K"},
		{re: regexp.MustCompile(`(?i)^gpt-4o|^gpt-4\.1|^gpt-5|^o[134]|^gpt-oss`), capacity: 128_000, compact: "128K"},
	}

	displayOverrides = map[string]string{
		"gemini-4-argon-trial":      "Gemini 4 Argon Trial",
		"gemini-4-carbon-trial":     "Gemini 4 Carbon Trial",
		"gemini-3.8-flash-control":  "Gemini 3.8 Flash",
		"gemini-3.8-flash-n":        "Gemini 3.8 Flash",
		"gemini-3.8-flash-tiered":   "Gemini 3.8 Flash",
		"gemini-3.8-flash-exp-a":    "Gemini 3.8 Flash",
		"gemini-3.7-flash-tiered":   "Gemini 3.7 Flash",
		"gemini-3.7-flash-safety-le": "Gemini 3.7 Flash",
		"gemini-3.6-flash-tiered":   "Gemini 3.6 Flash",
		"gemini-3.6-flash-exp-a":    "Gemini 3.6 Flash",
		"gemini-3-flash":            "Gemini 3 Flash",
		"gemini-3-flash-a":          "Gemini 3 Flash",
		"gemini-pro-default":        "Gemini Pro",
	}

	internalPlaceholderMap = map[string]string{
		"model_placeholder_m667":    "gemini-4-carbon",
		"model_placeholder_m668":    "gemini-4-argon",
		"model_placeholder_m669":    "gemini-4-hydrogen",
		"gemini-flash-worker":       "gemini-4-hydrogen",
		"gemini-flash-worker-agy":   "gemini-4-hydrogen",
	}

	cleanPrefixRe = regexp.MustCompile(`(?i)^models/`)
	trimDecorRe   = regexp.MustCompile(`^[+\s·•]+|[⌃⌄^\s]+$`)
	tierParenRe   = regexp.MustCompile(`(?i)[（(]\s*(?:Extra\s+High|Extra\s+Low|XHigh|High|Medium|Low|Max|Thinking|高|中|低|思考)\s*[）)]`)
	tierTailRe    = regexp.MustCompile(`(?i)\s+(?:Extra\s+High|Extra\s+Low|XHigh|High|Medium|Low|Max|Thinking)\b.*$`)
	tierSlugRe    = regexp.MustCompile(`(?:-(?:extra-high|extra-low|xhigh|high|medium|low|max|thinking|latest))+$`)
	parenGroupRe  = regexp.MustCompile(`（[^）]*）|\([^)]*\)`)
	nonSlugRe     = regexp.MustCompile(`[^a-z0-9.]+`)
	geminiDynRe   = regexp.MustCompile(`(?i)^gemini-([4-9](?:\.\d+)?)-([a-z][a-z0-9]*?)(?:-(trial|preview|exp))?$`)
	claudeDynRe   = regexp.MustCompile(`(?i)^claude-(opus|sonnet|haiku)-(\d+)-(\d+)$`)
	geminiCodeRe  = regexp.MustCompile(`(?i)^gemini-(?:[4-9](?:\.\d+)?)-([a-z][a-z0-9]*)`)
)

func initCatalog() {
	catalogOnce.Do(func() {
		catNames = make(map[string]string)
		catCaps = make(map[string]int64)
		catPrices = make(map[string]PriceRule)
		catAliases = make(map[string]string)

		if raw, err := resources.ReadFile("assets/model-catalog.json"); err == nil {
			var cat modelCatalogFile
			if json.Unmarshal(raw, &cat) == nil {
				for k, v := range cat.Names {
					catNames[strings.ToLower(k)] = v
				}
				for k, v := range cat.Capacities {
					catCaps[strings.ToLower(k)] = v
				}
				for k, v := range cat.Prices {
					catPrices[strings.ToLower(k)] = normalizePriceRule(v)
				}
				for k, v := range cat.Aliases {
					catAliases[strings.ToLower(k)] = strings.ToLower(v)
				}
			}
		}

		if raw, err := resources.ReadFile("assets/pricing-feed.json"); err == nil {
			mergeFeedBytes(raw)
		}
	})
}

func mergeFeedBytes(raw []byte) {
	var feed pricingFeedFile
	if json.Unmarshal(raw, &feed) != nil {
		return
	}
	for k, v := range feed.Names {
		if v != "" {
			catNames[strings.ToLower(k)] = v
		}
	}
	for k, v := range feed.Capacities {
		if v > 0 {
			catCaps[strings.ToLower(k)] = v
		}
	}
	for k, v := range feed.Aliases {
		if v != "" {
			catAliases[strings.ToLower(k)] = strings.ToLower(v)
		}
	}
	for k, rawEntry := range feed.Models {
		if pr, ok := parsePriceEntry(k, rawEntry); ok {
			catPrices[strings.ToLower(k)] = pr
		}
	}
	for k, rawEntry := range feed.Prices {
		if pr, ok := parsePriceEntry(k, rawEntry); ok {
			catPrices[strings.ToLower(k)] = pr
		}
	}
}

func parsePriceEntry(modelKey string, raw json.RawMessage) (PriceRule, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return PriceRule{}, false
	}
	if strings.HasPrefix(trimmed, "[") {
		var ivs []pricingInterval
		if json.Unmarshal(raw, &ivs) != nil || len(ivs) == 0 {
			return PriceRule{}, false
		}
		pr := PriceRule{
			Model:      modelKey,
			Input:      ivs[0].Input,
			Output:     ivs[0].Output,
			Cached:     ivs[0].Cached,
			CacheWrite: ivs[0].CacheWrite,
			Intervals:  ivs,
		}
		return normalizePriceRule(pr), true
	}
	var pr PriceRule
	if json.Unmarshal(raw, &pr) != nil {
		return PriceRule{}, false
	}
	if pr.Input == 0 && pr.Output == 0 && pr.Cached == 0 && len(pr.Intervals) == 0 {
		return PriceRule{}, false
	}
	if pr.Model == "" {
		pr.Model = modelKey
	}
	return normalizePriceRule(pr), true
}

func normalizePriceRule(p PriceRule) PriceRule {
	if p.Cached == 0 && p.CacheRead > 0 {
		p.Cached = p.CacheRead
	}
	return p
}

// LoadPricingOverrides merges optional remote/custom pricing JSON files from GravityBox stateDir.
func LoadPricingOverrides(stateDir string) {
	initCatalog()
	if stateDir == "" {
		return
	}
	for _, name := range []string{"remote_pricing.json", "api_pricing.json"} {
		p := filepath.Join(stateDir, name)
		if raw, err := os.ReadFile(p); err == nil {
			mergeFeedBytes(raw)
		}
	}
}

// NormalizeModelSlug cleans raw UI/database model strings into a canonical model slug.
func NormalizeModelSlug(raw string) string {
	initCatalog()
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = cleanPrefixRe.ReplaceAllString(s, "")
	s = trimDecorRe.ReplaceAllString(s, "")
	s = tierParenRe.ReplaceAllString(s, "")
	s = tierTailRe.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	low := strings.ToLower(s)
	if mapped, ok := internalPlaceholderMap[low]; ok {
		return mapped
	}
	if alias, ok := catAliases[low]; ok && alias != "" {
		low = alias
	}

	slug := parenGroupRe.ReplaceAllString(low, "")
	slug = nonSlugRe.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	slug = strings.TrimPrefix(slug, "models-")
	slug = tierSlugRe.ReplaceAllString(slug, "")
	if mapped, ok := internalPlaceholderMap[slug]; ok {
		return mapped
	}
	if alias, ok := catAliases[slug]; ok && alias != "" {
		return alias
	}
	return slug
}

func formatCompactCapacity(tokens int64) string {
	if tokens >= 1_000_000 {
		m := float64(tokens) / 1_000_000
		if math.Abs(m-math.Round(m)) < 1e-6 {
			return itoa64(int64(math.Round(m))) + "M"
		}
		return trimFloat(m, 1) + "M"
	}
	return itoa64((tokens+500)/1000) + "K"
}

func autoFormatDynamicModelName(slug string) string {
	if m := geminiDynRe.FindStringSubmatch(slug); len(m) >= 3 {
		ver := m[1]
		code := capFirst(m[2])
		suffix := ""
		if len(m) >= 4 && m[3] != "" {
			suffix = " " + capFirst(m[3])
		}
		return "Gemini " + ver + " " + code + suffix
	}
	if m := claudeDynRe.FindStringSubmatch(slug); len(m) == 4 {
		return "Claude " + capFirst(m[1]) + " " + m[2] + "." + m[3]
	}
	return ""
}

func capFirst(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// BuildCompactModelLabel builds short and single-char labels matching metrics-engine.ts.
func BuildCompactModelLabel(modelID, displayName string) (string, string) {
	lower := strings.ToLower(modelID)
	switch {
	case strings.Contains(lower, "carbon"):
		return "Carbon", "C"
	case strings.Contains(lower, "argon"):
		return "Argon", "A"
	case strings.Contains(lower, "hydrogen"):
		return "Hydrogen", "H"
	case strings.Contains(lower, "neon"):
		return "Neon", "N"
	case strings.Contains(lower, "pro") && strings.HasPrefix(lower, "gemini"):
		return "Pro", "P"
	case strings.Contains(lower, "flash") && strings.HasPrefix(lower, "gemini"):
		return "Flash", "F"
	case strings.HasPrefix(lower, "claude-") && strings.Contains(lower, "opus"):
		return "Opus", "O"
	case strings.HasPrefix(lower, "claude-") && strings.Contains(lower, "sonnet"):
		return "Sonnet", "S"
	case strings.HasPrefix(lower, "claude-") && strings.Contains(lower, "haiku"):
		return "Haiku", "H"
	case strings.HasPrefix(lower, "gpt-oss"):
		return "GPT-OSS", "G"
	}
	if m := geminiCodeRe.FindStringSubmatch(lower); len(m) == 2 {
		c := capFirst(m[1])
		return c, strings.ToUpper(c[:1])
	}
	cleaned := strings.TrimSpace(displayName)
	if cleaned == "" {
		cleaned = strings.TrimSpace(modelID)
	}
	if cleaned == "" {
		cleaned = "Model"
	}
	short := cleaned
	if len(short) > 10 {
		parts := strings.Fields(short)
		if len(parts) >= 2 {
			short = parts[0] + " " + parts[1]
		}
		if len(short) > 10 {
			short = short[:10]
		}
	}
	compact := "M"
	for i := 0; i < len(short); i++ {
		ch := short[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			compact = strings.ToUpper(string(ch))
			break
		}
	}
	return short, compact
}

// ResolveModelCapacity resolves canonical model ID, display name, short labels, and context threshold.
func ResolveModelCapacity(raw string) ModelCapacitySpec {
	initCatalog()
	cleanRaw := strings.TrimSpace(raw)
	if cleanRaw == "" {
		return ModelCapacitySpec{
			ModelID:         "gemini-3.8-flash",
			DisplayName:     "Gemini 3.8 Flash",
			ShortLabel:      "Flash",
			CompactLabel:    "F",
			CapacityTokens:  DefaultContextCapacityTokens,
			CapacityCompact: "200K",
			IsFallback:      false,
		}
	}
	slug := NormalizeModelSlug(cleanRaw)
	displayName := displayOverrides[slug]
	if displayName == "" {
		displayName = catNames[slug]
	}
	if displayName == "" {
		displayName = autoFormatDynamicModelName(slug)
	}
	if displayName == "" {
		displayName = cleanRaw
	}
	short, compact := BuildCompactModelLabel(slug, displayName)

	if capTok, ok := catCaps[slug]; ok && capTok > 0 {
		return ModelCapacitySpec{
			ModelID:         slug,
			DisplayName:     displayName,
			ShortLabel:      short,
			CompactLabel:    compact,
			CapacityTokens:  capTok,
			CapacityCompact: formatCompactCapacity(capTok),
			IsFallback:      false,
		}
	}
	for _, rule := range familyRules {
		if rule.re.MatchString(slug) {
			return ModelCapacitySpec{
				ModelID:         slug,
				DisplayName:     displayName,
				ShortLabel:      short,
				CompactLabel:    compact,
				CapacityTokens:  rule.capacity,
				CapacityCompact: rule.compact,
				IsFallback:      false,
			}
		}
	}
	if slug == "" {
		slug = "unknown-model"
	}
	return ModelCapacitySpec{
		ModelID:         slug,
		DisplayName:     displayName,
		ShortLabel:      short,
		CompactLabel:    compact,
		CapacityTokens:  DefaultContextCapacityTokens,
		CapacityCompact: "~200K",
		IsFallback:      true,
	}
}

// LookupPrice finds pricing rates (input, output, cacheRead, cacheWrite per 1M tokens) for a model.
func LookupPrice(rawModel string, stepTs float64) (inputRate, outputRate, cacheReadRate, cacheWriteRate float64, found bool) {
	initCatalog()
	slug := NormalizeModelSlug(rawModel)
	if slug == "" {
		return 0, 0, 0, 0, false
	}
	rule, ok := catPrices[slug]
	if !ok {
		for k, v := range catPrices {
			if strings.HasPrefix(slug, k) {
				rule = v
				ok = true
				break
			}
		}
	}
	if !ok {
		return 0, 0, 0, 0, false
	}
	in, out, cr, cw := rule.Input, rule.Output, rule.Cached, rule.CacheWrite
	if len(rule.Intervals) > 0 && stepTs > 0 {
		ts := time.Unix(int64(stepTs), 0).UTC()
		for _, iv := range rule.Intervals {
			if iv.ValidFrom != nil {
				if t, err := time.Parse(time.RFC3339, *iv.ValidFrom); err == nil && ts.Before(t) {
					continue
				}
			}
			if iv.ValidUntil != nil {
				if t, err := time.Parse(time.RFC3339, *iv.ValidUntil); err == nil && ts.After(t) {
					continue
				}
			}
			in, out, cr, cw = iv.Input, iv.Output, iv.Cached, iv.CacheWrite
			break
		}
	}
	return in, out, cr, cw, true
}
