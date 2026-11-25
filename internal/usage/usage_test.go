package usage

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"gravitybox-mygo/internal/config"
)

func resetUsageCachesForTest() {
	cacheMu.Lock()
	activeEmail = ""
	ledgerSig = ""
	ledgerEvents = nil
	ledgerDBKeys = nil
	ledgerEventKeys = nil
	convCache = make(map[string]convFileCacheEntry)
	convCacheLedgerSig = ""
	acctEmailByID = make(map[string]string)
	acctIDByEmail = make(map[string]string)
	acctMapSig = ""
	obsTimeline = nil
	obsSig = ""
	sessionObs = nil
	internTable = make(map[string]string)
	summaryCacheWorldSig = ""
	summaryCache = make(map[summaryCacheKey]Summary)
	cacheMu.Unlock()
}

func pbVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func pbVarintField(fn int, v uint64) []byte {
	out := pbVarint(uint64(fn<<3 | 0))
	return append(out, pbVarint(v)...)
}

func pbBytesField(fn int, data []byte) []byte {
	out := pbVarint(uint64(fn<<3 | 2))
	out = append(out, pbVarint(uint64(len(data)))...)
	return append(out, data...)
}

func TestAggregateSummaryCacheWriteAndHitRate(t *testing.T) {
	now := time.Now().Unix()
	events := []cachedUsageEvent{
		{
			Timestamp:        now,
			AccountEmail:     "a@example.com",
			Model:            "claude-sonnet-4-6",
			InputTokens:      100,
			CacheWriteTokens: 300,
			CachedTokens:     600,
			OutputTokens:     50,
			TotalTokens:      1050,
			EstimatedUSD:     0.25,
		},
	}

	s := aggregateSummary(events, 0, "all")
	if s.PeriodInput != 100 || s.PeriodCacheWrite != 300 || s.PeriodCached != 600 || s.PeriodOutput != 50 {
		t.Fatalf("unexpected period token breakdown: %+v", s)
	}
	if s.PeriodTokens != 1050 {
		t.Fatalf("expected PeriodTokens=1050, got %d", s.PeriodTokens)
	}
	// CacheHitRate = 600 / (100 + 300 + 600) * 100 = 60.0%
	if math.Abs(s.CacheHitRate-60.0) > 1e-6 {
		t.Fatalf("expected CacheHitRate=60.0, got %v", s.CacheHitRate)
	}
}

func TestLedgerDeduplicatesAndResolvesStepFallbackAndObservations(t *testing.T) {
	resetUsageCachesForTest()
	t.Cleanup(resetUsageCachesForTest)

	tmp := t.TempDir()
	appData := filepath.Join(tmp, "roaming")
	t.Setenv("APPDATA", appData)
	t.Setenv("XDG_CONFIG_HOME", appData)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	stateDir := config.StateDir()
	convDir := filepath.Join(config.GeminiDir(), "antigravity", "conversations")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Stale observation from 2 days ago (like legacy Electron ledger stopping writes)
	nowTime := time.Now()
	now := nowTime.Unix()
	dayStart := time.Date(nowTime.Year(), nowTime.Month(), nowTime.Day(), 0, 0, 0, 0, nowTime.Location()).Unix()
	todayTs1 := max(dayStart+30, now-3600)
	todayTs2 := max(dayStart+60, now-1800)
	obsJSON := `{"version":1,"entries":[{"at":` +
		pbNumStr(now-172800) + `,"id":"acct-old","email":"old@example.com"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "usage-account-observations.json"), []byte(obsJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build usage-ledger-v2.sqlite with two duplicate rows for conv1.db:0 (Windows backslash + bare filename)
	ledgerPath := filepath.Join(stateDir, "usage-ledger-v2.sqlite")
	ldb, err := sqlite.Open(ctx, ledgerPath, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE events(event_key TEXT PRIMARY KEY, timestamp INTEGER NOT NULL, account_id TEXT, model TEXT NOT NULL, payload TEXT NOT NULL, observed_at REAL NOT NULL DEFAULT 0)`,
		`CREATE TABLE rate_locks(event_key TEXT PRIMARY KEY, usd REAL NOT NULL)`,
		`CREATE TABLE event_aliases(legacy_key TEXT PRIMARY KEY, event_key TEXT NOT NULL)`,
		`INSERT INTO events(event_key, timestamp, account_id, model, payload) VALUES ('old-rev', ` + pbNumStr(todayTs1) + `, 'acct-old', 'gemini-4-hydrogen', '{"db_file":"C:\\\\path\\\\conv1.db","generation_index":0,"input_tokens":100,"output_tokens":50,"cached_tokens":200,"total_tokens":350,"account_email":"old@example.com"}')`,
		`INSERT INTO events(event_key, timestamp, account_id, model, payload) VALUES ('new-rev', ` + pbNumStr(todayTs2) + `, 'acct-old', 'gemini-4-hydrogen', '{"db_file":"conv1.db","generation_index":0,"input_tokens":120,"output_tokens":60,"cached_tokens":220,"total_tokens":400,"account_email":"old@example.com"}')`,
	} {
		if _, err = ldb.Query(ctx, stmt); err != nil {
			_ = ldb.Close()
			t.Fatal(err)
		}
	}
	_ = ldb.Close()

	// Live conversation DB: idx 0 mirrors ledger (should be skipped via cleanDBBase),
	// idx 1 is a new generation referencing a non-step-15 step (step_type = 23) with yesterday's timestamp
	// while file ModTime is today -> must use step timestamp (yesterday), NOT today's ModTime!
	yesterday := now - 86400
	dbPath := filepath.Join(convDir, "conv1.db")
	cdb, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER, status INTEGER, metadata BLOB)`,
		`CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY, data BLOB)`,
	} {
		if _, err = cdb.Query(ctx, stmt); err != nil {
			_ = cdb.Close()
			t.Fatal(err)
		}
	}

	tsBlob := append(pbVarintField(1, uint64(yesterday)), pbVarintField(2, 0)...)
	stepMeta := pbBytesField(1, tsBlob)
	if _, err := cdb.Query(ctx, "INSERT INTO steps(idx, step_type, status, metadata) VALUES (5, 23, 3, ?)", stepMeta); err != nil {
		_ = cdb.Close()
		t.Fatal(err)
	}

	makeGen := func(stepIdx int, inTok, outTok, cacheWrite, cacheRead uint64) []byte {
		u := append(pbVarintField(2, inTok), pbVarintField(3, outTok)...)
		if cacheWrite > 0 {
			u = append(u, pbVarintField(4, cacheWrite)...)
		}
		u = append(u, pbVarintField(5, cacheRead)...)
		chatMeta := pbBytesField(4, u)
		chatMeta = append(chatMeta, pbBytesField(19, []byte("gemini-4-hydrogen"))...)
		outer := pbBytesField(1, chatMeta)
		outer = append(outer, pbBytesField(2, pbVarint(uint64(stepIdx)))...)
		return outer
	}
	if _, err := cdb.Query(ctx, "INSERT INTO gen_metadata(idx, data) VALUES (0, ?), (1, ?)",
		makeGen(5, 120, 60, 0, 220),
		makeGen(5, 500, 100, 50, 400),
	); err != nil {
		_ = cdb.Close()
		t.Fatal(err)
	}
	_ = cdb.Close()

	// Set live session account switched to new@example.com
	SetActiveAccountEmail("new@example.com")

	sumAll := LoadSummary(ctx, 3, "all")
	// Expected unique events: 1 deduplicated ledger row (400) + 1 new DB row (1050) = 1450 tokens, 2 requests
	if sumAll.TotalRequests != 2 {
		t.Fatalf("expected 2 deduplicated requests, got %d", sumAll.TotalRequests)
	}
	if sumAll.TotalTokens != 1450 {
		t.Fatalf("expected TotalTokens=1450 (400 + 1050), got %d", sumAll.TotalTokens)
	}
	if sumAll.PeriodCacheWrite != 50 {
		t.Fatalf("expected PeriodCacheWrite=50, got %d", sumAll.PeriodCacheWrite)
	}

	// Non-step-15 timestamp was yesterday, so Today should have 1 request (the ledger row from now-1800), NOT 2!
	if sumAll.TodayRequests != 1 {
		t.Fatalf("expected TodayRequests=1 (non-step-15 DB row must stay yesterday, not file mtime), got %d", sumAll.TodayRequests)
	}

	// Filtering by new@example.com should find the new DB row even though stale disk observation was old@example.com!
	sumNewAcct := LoadSummary(ctx, 3, "new@example.com")
	if sumNewAcct.PeriodRequests != 1 || sumNewAcct.PeriodTokens != 1050 {
		t.Fatalf("expected new@example.com to match 1 req / 1050 tok, got reqs=%d tok=%d", sumNewAcct.PeriodRequests, sumNewAcct.PeriodTokens)
	}
}

func pbNumStr(v int64) string {
	return string(pbDec(v))
}

func pbDec(v int64) []byte {
	if v == 0 {
		return []byte{'0'}
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return buf[i:]
}

func TestLoadPricingTableHasRealPrices(t *testing.T) {
	entries := LoadPricingTable()
	if len(entries) < 5 {
		t.Fatalf("expected >= 5 pricing entries, got %d", len(entries))
	}
	foundNonZero := false
	for _, e := range entries {
		if e.InputPerM > 0 && e.OutputPerM > 0 {
			foundNonZero = true
			break
		}
	}
	if !foundNonZero {
		t.Fatal("expected non-zero input/output pricing entries in LoadPricingTable")
	}
}

func TestLiveUsageSummaryIfPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	t0 := time.Now()
	s0 := LoadSummary(ctx, 0, "all")
	t.Logf("Today: cold=%s tokens=%d reqs=%d usd=%.2f", time.Since(t0), s0.TodayTokens, s0.TodayRequests, s0.TodayUSD)

	t1 := time.Now()
	sAll := LoadSummary(ctx, 3, "all")
	t.Logf("All(warm): elapsed=%s totalTokens=%d totalReqs=%d totalUSD=%.2f models=%d",
		time.Since(t1), sAll.TotalTokens, sAll.TotalRequests, sAll.TotalUSD, len(sAll.Models))
}

func TestSummaryCacheInvalidatesOnSessionAccountSwitch(t *testing.T) {
	resetUsageCachesForTest()
	t.Cleanup(resetUsageCachesForTest)

	tmp := t.TempDir()
	appData := filepath.Join(tmp, "roaming")
	t.Setenv("APPDATA", appData)
	t.Setenv("XDG_CONFIG_HOME", appData)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	stateDir := config.StateDir()
	convDir := filepath.Join(config.GeminiDir(), "antigravity", "conversations")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbPath := filepath.Join(convDir, "cache-test.db")
	cdb, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER, status INTEGER, metadata BLOB)`,
		`CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY, data BLOB)`,
	} {
		if _, err = cdb.Query(ctx, stmt); err != nil {
			_ = cdb.Close()
			t.Fatal(err)
		}
	}
	now := time.Now().Unix()
	eventTs := now - 5
	tsBlob := append(pbVarintField(1, uint64(eventTs)), pbVarintField(2, 0)...)
	stepMeta := pbBytesField(1, tsBlob)
	if _, err := cdb.Query(ctx, "INSERT INTO steps(idx, step_type, status, metadata) VALUES (1, 15, 3, ?)", stepMeta); err != nil {
		_ = cdb.Close()
		t.Fatal(err)
	}
	u := append(pbVarintField(2, 200), pbVarintField(3, 50)...)
	chatMeta := pbBytesField(4, u)
	chatMeta = append(chatMeta, pbBytesField(19, []byte("gemini-4-hydrogen"))...)
	outer := pbBytesField(1, chatMeta)
	outer = append(outer, pbBytesField(2, pbVarint(1))...)
	if _, err := cdb.Query(ctx, "INSERT INTO gen_metadata(idx, data) VALUES (0, ?)", outer); err != nil {
		_ = cdb.Close()
		t.Fatal(err)
	}
	_ = cdb.Close()

	SetActiveAccountEmail("first@example.com")
	sBefore := LoadSummary(ctx, 0, "second@example.com")
	if sBefore.PeriodRequests != 0 {
		t.Fatalf("expected second@example.com=0 reqs before switch, got %d", sBefore.PeriodRequests)
	}

	// Switching session account must invalidate summary cache even with zero disk changes.
	SetActiveAccountEmail("second@example.com")
	sAfter := LoadSummary(ctx, 0, "second@example.com")
	if sAfter.PeriodRequests != 1 || sAfter.PeriodTokens != 250 {
		t.Fatalf("expected second@example.com=1 req/250 tok after switch, got reqs=%d tok=%d", sAfter.PeriodRequests, sAfter.PeriodTokens)
	}
}
