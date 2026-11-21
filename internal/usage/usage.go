package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/hud"
	"gravitybox-mygo/resources"
)

// ModelSummary summarizes token, cost, and request stats for one model.
type ModelSummary struct {
	Model        string
	TotalTokens  int64
	RequestCount int64
	EstimatedUSD float64
}

// ChartBucket represents one bar in the usage distribution chart.
type ChartBucket struct {
	Label  string
	Tokens int64
}

// EventRecord represents a single recent API usage event for the request details dialog.
type EventRecord struct {
	Timestamp    int64
	Model        string
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
	TotalTokens  int64
	EstimatedUSD float64
}

// PriceEntry describes one model's token pricing per 1M tokens.
type PriceEntry struct {
	Model      string  `json:"model"`
	InputPerM  float64 `json:"input"`
	OutputPerM float64 `json:"output"`
	CacheReadM float64 `json:"cache_read"`
}

// Summary holds aggregated usage metrics for both Overview and Usage pages.
type Summary struct {
	// Overview quick stats (all accounts, today vs total)
	TodayTokens   int64
	TotalTokens   int64
	TodayUSD      float64
	TotalUSD      float64
	TodayRequests int64
	TotalRequests int64
	TodayHourly   [24]int64
	TodayModels   []ModelSummary

	// Filtered Usage page stats (by period + accountFilter)
	PeriodTokens    int64
	PeriodInput     int64
	PeriodCached    int64
	PeriodOutput    int64
	PeriodUSD       float64
	PeriodRequests  int64
	AvgTokensPerReq int64
	CacheHitRate    float64
	Buckets         []ChartBucket
	Models          []ModelSummary
	RecentEvents    []EventRecord
}

type cachedUsageEvent struct {
	Timestamp        int64
	AccountID        string
	AccountEmail     string
	Model            string
	InputTokens      int64
	CachedTokens     int64
	CacheWriteTokens int64
	OutputTokens     int64
	TotalTokens      int64
	EstimatedUSD     float64
	DBFile           string
	GenIdx           int
	HasGenIdx        bool
}

type convFileCacheEntry struct {
	sig    string
	events []cachedUsageEvent
}

type observationEntry struct {
	At    int64  `json:"at"`
	ID    string `json:"id"`
	Email string `json:"email"`
}

var (
	cacheMu          sync.Mutex
	activeEmail      string
	ledgerSig        string
	ledgerEvents     []cachedUsageEvent
	ledgerDBKeys     map[string]bool
	ledgerEventKeys  map[string]bool
	convCache        = make(map[string]convFileCacheEntry)
	acctEmailByID    = make(map[string]string)
	acctIDByEmail    = make(map[string]string)
	acctMapSig       string
	obsTimeline      []observationEntry
	obsSig           string
)

// SetActiveAccountEmail updates the fallback active email attributed to newly scanned conversation events.
func SetActiveAccountEmail(email string) {
	cacheMu.Lock()
	activeEmail = strings.TrimSpace(email)
	cacheMu.Unlock()
}

// LoadSummary reads both %APPDATA%\GravityBox\usage-ledger-v2.sqlite (cached by mtime)
// and recent ~/.gemini/antigravity/conversations/*.db generations incrementally.
// period: 0=Today, 1=Last 7 Days, 2=Last 30 Days, 3=All Time.
func LoadSummary(ctx context.Context, period int, accountFilter string) Summary {
	hud.LoadPricingOverrides(config.StateDir())
	allEvents := loadMergedEvents(ctx)
	return aggregateSummary(allEvents, period, accountFilter)
}

func loadMergedEvents(ctx context.Context) []cachedUsageEvent {
	refreshAccountLookup()
	refreshObservations()

	lEvents, lDBKeys, lTriplets := loadLedgerCached(ctx)
	cEvents := scanConversationDBsCached(ctx)

	cacheMu.Lock()
	fallbackEmail := activeEmail
	obs := obsTimeline
	cacheMu.Unlock()

	merged := make([]cachedUsageEvent, 0, len(lEvents)+len(cEvents))
	merged = append(merged, lEvents...)

	for _, ev := range cEvents {
		if ev.HasGenIdx && ev.DBFile != "" {
			if lDBKeys[ev.DBFile+":"+strconv.Itoa(ev.GenIdx)] {
				continue
			}
		}
		if lTriplets[tripletKey(ev.Timestamp, ev.Model, ev.TotalTokens)] {
			continue
		}
		if ev.AccountEmail == "" {
			if em, aid := lookupObservation(obs, ev.Timestamp); em != "" {
				ev.AccountEmail = em
				ev.AccountID = aid
			} else if fallbackEmail != "" {
				ev.AccountEmail = fallbackEmail
			}
		}
		if ev.EstimatedUSD <= 0 {
			ev.EstimatedUSD = estimateEventCost(ev.Model, float64(ev.Timestamp), ev.InputTokens, ev.CachedTokens, ev.CacheWriteTokens, ev.OutputTokens)
		}
		merged = append(merged, ev)
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Timestamp > merged[j].Timestamp
	})
	return merged
}

func tripletKey(ts int64, model string, total int64) string {
	return strconv.FormatInt(ts, 10) + "|" + model + "|" + strconv.FormatInt(total, 10)
}

func loadLedgerCached(ctx context.Context) ([]cachedUsageEvent, map[string]bool, map[string]bool) {
	dbPath := filepath.Join(config.StateDir(), "usage-ledger-v2.sqlite")
	sig := fileStatSig(dbPath)
	if sig == "" {
		return nil, map[string]bool{}, map[string]bool{}
	}

	cacheMu.Lock()
	if ledgerSig == sig && ledgerEvents != nil {
		evs := ledgerEvents
		dbKeys := ledgerDBKeys
		triplets := ledgerEventKeys
		cacheMu.Unlock()
		return evs, dbKeys, triplets
	}
	cacheMu.Unlock()

	db, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{ReadOnly: true})
	if err != nil {
		return nil, map[string]bool{}, map[string]bool{}
	}
	defer db.Close()

	rows, err := db.Query(ctx, `
		SELECT e.timestamp, e.account_id, e.model, e.payload, COALESCE(l.usd, -1)
		FROM events e
		LEFT JOIN rate_locks l ON l.event_key = e.event_key
		LEFT JOIN event_aliases a ON a.legacy_key = e.event_key
		WHERE a.legacy_key IS NULL
		ORDER BY e.timestamp DESC`)
	if err != nil {
		rows, err = db.Query(ctx, `SELECT timestamp, account_id, model, payload, -1 FROM events ORDER BY timestamp DESC`)
		if err != nil {
			return nil, map[string]bool{}, map[string]bool{}
		}
	}

	evs := make([]cachedUsageEvent, 0, len(rows.Values))
	dbKeys := make(map[string]bool, len(rows.Values))
	triplets := make(map[string]bool, len(rows.Values))

	for _, r := range rows.Values {
		if len(r) < 4 {
			continue
		}
		ts := toInt64(r[0])
		acctID, _ := r[1].(string)
		model, _ := r[2].(string)
		payloadStr, _ := r[3].(string)
		if payloadStr == "" {
			continue
		}
		var ev struct {
			Model            string  `json:"model"`
			AccountID        string  `json:"account_id"`
			AccountEmail     string  `json:"account_email"`
			InputTokens      int64   `json:"input_tokens"`
			CachedTokens     int64   `json:"cached_tokens"`
			CacheWriteTokens int64   `json:"cache_write_tokens"`
			OutputTokens     int64   `json:"output_tokens"`
			TotalTokens      int64   `json:"total_tokens"`
			EstimatedUSD     float64 `json:"estimated_usd"`
			DBFile           string  `json:"db_file"`
			GenerationIndex  *int    `json:"generation_index"`
		}
		if json.Unmarshal([]byte(payloadStr), &ev) != nil {
			continue
		}
		if model == "" {
			model = ev.Model
		}
		if slug := hud.NormalizeModelSlug(model); slug != "" {
			model = slug
		} else if model == "" {
			model = "unknown"
		}
		if acctID == "" {
			acctID = ev.AccountID
		}
		email := ev.AccountEmail
		if email == "" && acctID != "" {
			cacheMu.Lock()
			email = acctEmailByID[acctID]
			cacheMu.Unlock()
		}
		evUSD := ev.EstimatedUSD
		if len(r) >= 5 {
			if locked := toFloat64(r[4]); locked >= 0 {
				evUSD = locked
			}
		}
		if evUSD <= 0 {
			evUSD = estimateEventCost(model, float64(ts), ev.InputTokens, ev.CachedTokens, ev.CacheWriteTokens, ev.OutputTokens)
		}

		item := cachedUsageEvent{
			Timestamp:        ts,
			AccountID:        acctID,
			AccountEmail:     email,
			Model:            model,
			InputTokens:      ev.InputTokens,
			CachedTokens:     ev.CachedTokens,
			CacheWriteTokens: ev.CacheWriteTokens,
			OutputTokens:     ev.OutputTokens,
			TotalTokens:      ev.TotalTokens,
			EstimatedUSD:     evUSD,
			DBFile:           ev.DBFile,
		}
		if ev.GenerationIndex != nil {
			item.GenIdx = *ev.GenerationIndex
			item.HasGenIdx = true
			if ev.DBFile != "" {
				dbKeys[ev.DBFile+":"+strconv.Itoa(*ev.GenerationIndex)] = true
			}
		}
		triplets[tripletKey(ts, model, ev.TotalTokens)] = true
		evs = append(evs, item)
	}

	cacheMu.Lock()
	ledgerSig = sig
	ledgerEvents = evs
	ledgerDBKeys = dbKeys
	ledgerEventKeys = triplets
	cacheMu.Unlock()

	return evs, dbKeys, triplets
}

func scanConversationDBsCached(ctx context.Context) []cachedUsageEvent {
	convDir := filepath.Join(config.GeminiDir(), "antigravity", "conversations")
	entries, err := os.ReadDir(convDir)
	if err != nil {
		return nil
	}

	var out []cachedUsageEvent
	for _, ent := range entries {
		if ctx.Err() != nil {
			break
		}
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".db") {
			continue
		}
		fullPath := filepath.Join(convDir, name)
		sig := fileStatSig(fullPath)
		if sig == "" {
			continue
		}

		cacheMu.Lock()
		cached, ok := convCache[fullPath]
		cacheMu.Unlock()
		if ok && cached.sig == sig {
			out = append(out, cached.events...)
			continue
		}

		evs := scanSingleConversationDB(ctx, fullPath, name)
		cacheMu.Lock()
		convCache[fullPath] = convFileCacheEntry{sig: sig, events: evs}
		cacheMu.Unlock()
		out = append(out, evs...)
	}
	return out
}

func scanSingleConversationDB(ctx context.Context, dbPath, dbFile string) []cachedUsageEvent {
	qctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	db, err := sqlite.Open(qctx, dbPath, sqlite.OpenOptions{ReadOnly: true})
	if err != nil {
		return nil
	}
	defer db.Close()

	_, _ = db.Query(qctx, "PRAGMA query_only = ON; PRAGMA busy_timeout = 300")

	grows, err := db.Query(qctx, "SELECT idx, data FROM gen_metadata WHERE data IS NOT NULL ORDER BY idx ASC")
	if err != nil {
		return nil
	}

	var stepTimes map[int]int64
	loadStepTimes := func() map[int]int64 {
		if stepTimes != nil {
			return stepTimes
		}
		stepTimes = make(map[int]int64)
		if srows, err := db.Query(qctx, "SELECT idx, step_type, status, metadata FROM steps WHERE step_type = 15 AND metadata IS NOT NULL ORDER BY idx ASC"); err == nil {
			for _, sr := range srows.Values {
				if len(sr) < 4 {
					continue
				}
				ps := hud.ParseStepMetadataBlob(toInt(sr[0]), toInt(sr[1]), toInt(sr[2]), toBytesVal(sr[3]), nil, nil, nil)
				if ps.CreatedAtSeconds != nil && *ps.CreatedAtSeconds >= 946684800 {
					stepTimes[ps.Idx] = int64(*ps.CreatedAtSeconds)
				} else if ps.StartTimeSeconds != nil && *ps.StartTimeSeconds >= 946684800 {
					stepTimes[ps.Idx] = int64(*ps.StartTimeSeconds)
				}
			}
		}
		return stepTimes
	}

	fallbackTs := int64(0)
	if info, err := os.Stat(dbPath); err == nil {
		fallbackTs = info.ModTime().Unix()
	}

	var events []cachedUsageEvent
	for _, r := range grows.Values {
		if len(r) < 2 {
			continue
		}
		genIdx := toInt(r[0])
		blob := toBytesVal(r[1])
		if len(blob) == 0 {
			continue
		}
		g := hud.ParseGenMetadataBlob(blob)
		if !g.HasUsage {
			continue
		}
		totalTok := g.RawInputTokens + g.CachedReadTokens + g.CacheWriteTokens + g.OutputTokens
		if totalTok <= 0 {
			continue
		}

		ts := int64(g.StartedAtSeconds)
		if ts < 946684800 {
			stMap := loadStepTimes()
			for _, sIdx := range g.StepIndices {
				if v, ok := stMap[sIdx]; ok && v >= 946684800 {
					ts = v
					break
				}
			}
		}
		if ts < 946684800 {
			ts = fallbackTs
		}

		rawModel := g.ResponseModel
		if rawModel == "" {
			rawModel = g.ModelDisplayName
		}
		if rawModel == "" {
			rawModel = g.PlannerModelName
		}
		if rawModel == "" {
			switch g.ModelEnum {
			case 1667:
				rawModel = "gemini-4-carbon"
			case 1668:
				rawModel = "gemini-4-argon"
			case 1669:
				rawModel = "gemini-4-hydrogen"
			}
		}
		model := hud.NormalizeModelSlug(rawModel)
		if model == "" {
			model = "unknown"
		}

		events = append(events, cachedUsageEvent{
			Timestamp:        ts,
			Model:            model,
			InputTokens:      g.RawInputTokens,
			CachedTokens:     g.CachedReadTokens,
			CacheWriteTokens: g.CacheWriteTokens,
			OutputTokens:     g.OutputTokens,
			TotalTokens:      totalTok,
			DBFile:           dbFile,
			GenIdx:           genIdx,
			HasGenIdx:        true,
		})
	}
	return events
}

func aggregateSummary(events []cachedUsageEvent, period int, accountFilter string) Summary {
	var out Summary
	now := time.Now()
	loc := now.Location()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).Unix()
	var periodStart int64
	switch period {
	case 0:
		periodStart = dayStart
	case 1:
		periodStart = time.Date(now.Year(), now.Month(), now.Day()-6, 0, 0, 0, 0, loc).Unix()
	case 2:
		periodStart = time.Date(now.Year(), now.Month(), now.Day()-29, 0, 0, 0, 0, loc).Unix()
	default:
		periodStart = 0
	}

	filterLow := strings.ToLower(strings.TrimSpace(accountFilter))
	filterID := ""
	if filterLow != "" && filterLow != "all" {
		cacheMu.Lock()
		filterID = acctIDByEmail[filterLow]
		cacheMu.Unlock()
	}

	todayByModel := map[string]*ModelSummary{}
	periodByModel := map[string]*ModelSummary{}
	dailyBuckets := map[string]int64{}
	var todayFilteredHourly [24]int64

	for _, ev := range events {
		out.TotalTokens += ev.TotalTokens
		out.TotalUSD += ev.EstimatedUSD
		out.TotalRequests++
		if ev.Timestamp >= dayStart {
			out.TodayTokens += ev.TotalTokens
			out.TodayUSD += ev.EstimatedUSD
			out.TodayRequests++
			hr := time.Unix(ev.Timestamp, 0).In(loc).Hour()
			if hr >= 0 && hr < 24 {
				out.TodayHourly[hr] += ev.TotalTokens
			}
			tm := todayByModel[ev.Model]
			if tm == nil {
				tm = &ModelSummary{Model: ev.Model}
				todayByModel[ev.Model] = tm
			}
			tm.TotalTokens += ev.TotalTokens
			tm.EstimatedUSD += ev.EstimatedUSD
			tm.RequestCount++
		}

		if filterLow != "" && filterLow != "all" {
			matched := strings.EqualFold(ev.AccountEmail, accountFilter) ||
				strings.EqualFold(ev.AccountID, accountFilter) ||
				(filterID != "" && strings.EqualFold(ev.AccountID, filterID))
			if !matched {
				continue
			}
		}

		if ev.Timestamp >= dayStart {
			hr := time.Unix(ev.Timestamp, 0).In(loc).Hour()
			if hr >= 0 && hr < 24 {
				todayFilteredHourly[hr] += ev.TotalTokens
			}
		}

		if ev.Timestamp < periodStart {
			continue
		}

		out.PeriodTokens += ev.TotalTokens
		out.PeriodInput += ev.InputTokens
		out.PeriodCached += ev.CachedTokens
		out.PeriodOutput += ev.OutputTokens
		out.PeriodUSD += ev.EstimatedUSD
		out.PeriodRequests++

		pm := periodByModel[ev.Model]
		if pm == nil {
			pm = &ModelSummary{Model: ev.Model}
			periodByModel[ev.Model] = pm
		}
		pm.TotalTokens += ev.TotalTokens
		pm.EstimatedUSD += ev.EstimatedUSD
		pm.RequestCount++

		if period != 0 {
			dayKey := time.Unix(ev.Timestamp, 0).In(loc).Format("01-02")
			dailyBuckets[dayKey] += ev.TotalTokens
		}
		if len(out.RecentEvents) < 80 {
			out.RecentEvents = append(out.RecentEvents, EventRecord{
				Timestamp:    ev.Timestamp,
				Model:        ev.Model,
				InputTokens:  ev.InputTokens,
				CachedTokens: ev.CachedTokens,
				OutputTokens: ev.OutputTokens,
				TotalTokens:  ev.TotalTokens,
				EstimatedUSD: ev.EstimatedUSD,
			})
		}
	}

	out.TodayModels = sortModels(todayByModel)
	out.Models = sortModels(periodByModel)
	if out.PeriodRequests > 0 {
		out.AvgTokensPerReq = out.PeriodTokens / out.PeriodRequests
	}
	denom := out.PeriodInput + out.PeriodCached
	if denom > 0 {
		out.CacheHitRate = float64(out.PeriodCached) * 100 / float64(denom)
	}

	if period == 0 {
		out.Buckets = make([]ChartBucket, 24)
		for h := 0; h < 24; h++ {
			out.Buckets[h] = ChartBucket{Label: fmt.Sprintf("%02d时", h), Tokens: todayFilteredHourly[h]}
		}
	} else {
		days := 7
		if period == 2 {
			days = 30
		} else if period == 3 {
			days = min(30, max(7, len(dailyBuckets)))
		}
		out.Buckets = make([]ChartBucket, 0, days)
		for i := days - 1; i >= 0; i-- {
			d := now.AddDate(0, 0, -i).Format("01-02")
			out.Buckets = append(out.Buckets, ChartBucket{Label: d, Tokens: dailyBuckets[d]})
		}
	}
	return out
}

func refreshAccountLookup() {
	path := filepath.Join(config.StateDir(), "accounts.json")
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	sig := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
	cacheMu.Lock()
	if acctMapSig == sig {
		cacheMu.Unlock()
		return
	}
	cacheMu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var idx struct {
		Accounts []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"accounts"`
	}
	if json.Unmarshal(raw, &idx) != nil {
		return
	}
	byID := make(map[string]string, len(idx.Accounts))
	byEmail := make(map[string]string, len(idx.Accounts))
	for _, a := range idx.Accounts {
		if a.ID != "" && a.Email != "" {
			byID[a.ID] = a.Email
			byEmail[strings.ToLower(a.Email)] = a.ID
		}
	}
	cacheMu.Lock()
	acctMapSig = sig
	acctEmailByID = byID
	acctIDByEmail = byEmail
	cacheMu.Unlock()
}

func refreshObservations() {
	path := filepath.Join(config.StateDir(), "usage-account-observations.json")
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	sig := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
	cacheMu.Lock()
	if obsSig == sig {
		cacheMu.Unlock()
		return
	}
	cacheMu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f struct {
		Entries []observationEntry `json:"entries"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	sort.Slice(f.Entries, func(i, j int) bool {
		return f.Entries[i].At < f.Entries[j].At
	})
	cacheMu.Lock()
	obsSig = sig
	obsTimeline = f.Entries
	cacheMu.Unlock()
}

func lookupObservation(obs []observationEntry, ts int64) (email, id string) {
	for i := len(obs) - 1; i >= 0; i-- {
		if obs[i].At <= ts && obs[i].Email != "" {
			return obs[i].Email, obs[i].ID
		}
	}
	return "", ""
}

func fileStatSig(path string) string {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	walSize := int64(0)
	walMt := int64(0)
	if ws, err := os.Stat(path + "-wal"); err == nil {
		walSize = ws.Size()
		walMt = ws.ModTime().UnixNano()
	}
	return fmt.Sprintf("%d:%d:%d:%d", st.Size(), st.ModTime().UnixNano(), walSize, walMt)
}

func estimateEventCost(model string, tsSec float64, rawInput, cachedRead, cacheWrite, output int64) float64 {
	inR, outR, crR, cwR, ok := hud.LookupPrice(model, tsSec)
	if !ok {
		return 0
	}
	return (float64(rawInput)*inR + float64(cachedRead)*crR + float64(cacheWrite)*cwR + float64(output)*outR) / 1_000_000.0
}

// LoadPricingTable reads embedded and remote-synced model pricing entries from catalog + pricing-feed formats.
func LoadPricingTable() []PriceEntry {
	hud.LoadPricingOverrides(config.StateDir())
	byModel := make(map[string]PriceEntry)

	mergeRaw := func(raw []byte) {
		var cat struct {
			Names  map[string]string `json:"names"`
			Prices map[string]json.RawMessage `json:"prices"`
		}
		if json.Unmarshal(raw, &cat) != nil {
			return
		}
		for name := range cat.Names {
			if inR, outR, crR, _, ok := hud.LookupPrice(name, float64(time.Now().Unix())); ok {
				byModel[name] = PriceEntry{Model: name, InputPerM: inR, OutputPerM: outR, CacheReadM: crR}
			}
		}
		for modelName := range cat.Prices {
			if inR, outR, crR, _, ok := hud.LookupPrice(modelName, float64(time.Now().Unix())); ok {
				byModel[modelName] = PriceEntry{Model: modelName, InputPerM: inR, OutputPerM: outR, CacheReadM: crR}
			}
		}
	}

	if b, err := resources.ReadFile("assets/model-catalog.json"); err == nil {
		mergeRaw(b)
	}
	if b, err := resources.ReadFile("assets/pricing-feed.json"); err == nil {
		mergeRaw(b)
	}
	if b, err := os.ReadFile(filepath.Join(config.StateDir(), "remote_pricing.json")); err == nil {
		mergeRaw(b)
	}

	out := make([]PriceEntry, 0, len(byModel))
	for _, entry := range byModel {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// CheckRemotePricing fetches the latest public pricing feed from upstream GitHub.
func CheckRemotePricing(ctx context.Context) (string, error) {
	urls := []string{
		"https://raw.githubusercontent.com/KAIKAIKAISER/gravitybox/main/assets/pricing-feed.json",
		"https://raw.githubusercontent.com/Enzoo404/gravitybox/main/assets/pricing-feed.json",
	}
	var lastErr error
	for _, u := range urls {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		raw, rErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK || rErr != nil {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		var check map[string]any
		if json.Unmarshal(raw, &check) != nil {
			lastErr = fmt.Errorf("定价文件格式无效")
			continue
		}
		if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(config.StateDir(), "remote_pricing.json"), raw, 0o600); err != nil {
			return "", err
		}
		hud.LoadPricingOverrides(config.StateDir())
		return "已同步最新模型价格基准", nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("同步价格失败")
}

// ExportJSON writes the filtered usage summary to %APPDATA%\GravityBox\reports\ and returns the file path.
func ExportJSON(s Summary) (string, error) {
	dir := filepath.Join(config.StateDir(), "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("usage-export-%s.json", time.Now().Format("20060102-150405")))
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o600)
}

func sortModels(m map[string]*ModelSummary) []ModelSummary {
	res := make([]ModelSummary, 0, len(m))
	for _, v := range m {
		res = append(res, *v)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].TotalTokens > res[j].TotalTokens
	})
	return res
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	default:
		return 0
	}
}

func toFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	default:
		return -1
	}
}

func toBytesVal(v any) []byte {
	switch b := v.(type) {
	case []byte:
		return b
	case string:
		return []byte(b)
	default:
		return nil
	}
}

// FormatTokens formats token counts into K/M/B strings matching upstream.
func FormatTokens(v int64) string {
	switch {
	case v >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(v)/1e9)
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(v)/1e6)
	case v >= 1_000:
		return fmt.Sprintf("%.1fK", float64(v)/1e3)
	default:
		return fmt.Sprintf("%d", v)
	}
}

// FormatUSD formats USD estimate cleanly.
func FormatUSD(v float64) string {
	if v <= 0 {
		return "$0.00"
	}
	if v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}
