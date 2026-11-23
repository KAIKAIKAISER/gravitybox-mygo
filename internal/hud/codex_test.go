package hud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

func TestParseCodexGenRowAndTurnSummaries(t *testing.T) {
	usage := append(pbVarintField(2, 2400), pbVarintField(3, 600)...)
	usage = append(usage, pbVarintField(4, 150)...)
	usage = append(usage, pbVarintField(5, 18000)...)
	usage = append(usage, pbVarintField(9, 250)...)
	usage = append(usage, pbVarintField(10, 350)...)

	ctxMeta := pbVarintField(4, 512000)
	chatStart := pbBytesField(10, ctxMeta)
	ttft := append(pbVarintField(1, 1), pbVarintField(2, 250_000_000)...)
	stream := append(pbVarintField(1, 2), pbVarintField(2, 500_000_000)...)
	meta17 := append(pbVarintField(5, 1), pbBytesField(3, []byte("Model unavailable"))...)

	var chat []byte
	chat = append(chat, pbBytesField(4, usage)...)
	chat = append(chat, pbBytesField(9, chatStart)...)
	chat = append(chat, pbBytesField(11, ttft)...)
	chat = append(chat, pbBytesField(12, stream)...)
	chat = append(chat, pbBytesField(21, []byte("Gemini 4 Hydrogen (XHigh)"))...)
	chat = append(chat, pbBytesField(17, meta17)...)

	var packed []byte
	packed = append(packed, encodeVarint(14)...)
	packed = append(packed, encodeVarint(16)...)
	packed = append(packed, encodeVarint(15)...)

	top := append(pbBytesField(1, chat), pbBytesField(2, packed)...)
	parsed := ParseCodexGenRow(7, top)

	if parsed.Idx != 7 || parsed.InTok != 2400 || parsed.OutTok != 600 || parsed.CacheWrite != 150 || parsed.CacheRead != 18000 {
		t.Fatalf("unexpected token fields: %+v", parsed)
	}
	if parsed.Ctx != 20550 || parsed.MaxCtx != 512000 || parsed.MaxStepIdx != 16 {
		t.Fatalf("unexpected context/step fields: %+v", parsed)
	}
	if parsed.ModelName != "Gemini 4 Hydrogen (XHigh)" || parsed.ThinkTok != 250 || parsed.RespTok != 350 {
		t.Fatalf("unexpected model/output split: %+v", parsed)
	}
	if parsed.TTFT != 1.25 || parsed.Stream != 2.5 || parsed.TotDur != 3.75 {
		t.Fatalf("unexpected duration fields: %+v", parsed)
	}
	if parsed.ErrCode != 1 || parsed.ErrMsg != "Model unavailable" {
		t.Fatalf("unexpected error metadata: %+v", parsed)
	}

	turns := BuildCodexTurnSummaries([]CodexGenItem{parsed}, []int{10}, 0, 256000)
	if len(turns) != 1 || turns[0].TurnIndex != 1 || turns[0].Steps != 1 {
		t.Fatalf("unexpected turn summaries: %+v", turns)
	}
	if turns[0].Tps != 240 || turns[0].LimitStr != "512k" {
		t.Fatalf("unexpected turn throughput/limit: %+v", turns[0])
	}
}

func TestReadRecentLsRetryInfoAndStep17Extraction(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "language_server.log")
	now := time.Now()
	line1 := fmt.Sprintf("ERROR: logging before google.Init: I%02d%02d %02d:%02d:%02d.123456 999 executor.go:42] Run: attempt 6 failed (Model unavailable), retrying in 13s\n",
		now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second())
	line2 := fmt.Sprintf("I%02d%02d %02d:%02d:%02d.999999 999 llm_monitor.go:10] play.googleapis.com/log CallWithRetry: attempt 1 failed (noise), retrying in 1s\n",
		now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second())
	if err := os.WriteFile(logPath, []byte(line1+line2), 0o600); err != nil {
		t.Fatal(err)
	}
	info := ReadRecentLsRetryInfo(logPath, now.Add(-10*time.Second).UnixMilli(), now)
	if info == nil || info.Attempt != 6 || info.Reason != "Model unavailable" || info.Delay != "13s" {
		t.Fatalf("unexpected retry info: %+v", info)
	}

	errPayload := pbBytesField(1, append(
		pbBytesField(1, []byte("API error: TLS handshake timeout")),
		pbBytesField(2, []byte("a1a51091-fd1b-437d-a034-e4dbc428c4ca-17"))...,
	))
	msg := ExtractStep17Message(errPayload, nil)
	if msg != "API error: TLS handshake timeout" {
		t.Fatalf("unexpected step17 message: %q", msg)
	}
}

func TestCodexMetricsEngineAndBridgeStreaming(t *testing.T) {
	tmp := t.TempDir()
	convID := "22222222-2222-2222-2222-222222222222"
	dbPath := filepath.Join(tmp, convID+".db")

	ctx := context.Background()
	db, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	t0 := append(pbVarintField(1, 1763500000), pbVarintField(2, 0)...)
	metaUser := pbBytesField(1, t0)

	usage := append(pbVarintField(2, 2000), pbVarintField(3, 500)...)
	usage = append(usage, pbVarintField(5, 18000)...)
	usage = append(usage, pbVarintField(9, 200)...)
	usage = append(usage, pbVarintField(10, 300)...)
	chatStart := pbBytesField(10, pbVarintField(4, 512000))
	ttft := append(pbVarintField(1, 1), pbVarintField(2, 0)...)
	stream := append(pbVarintField(1, 2), pbVarintField(2, 0)...)
	var chat []byte
	chat = append(chat, pbBytesField(4, usage)...)
	chat = append(chat, pbBytesField(9, chatStart)...)
	chat = append(chat, pbBytesField(11, ttft)...)
	chat = append(chat, pbBytesField(12, stream)...)
	chat = append(chat, pbBytesField(21, []byte("Gemini 4 Carbon (Medium)"))...)
	top := append(pbBytesField(1, chat), pbBytesField(2, encodeVarint(1))...)

	_, err = db.Transaction(ctx, []sqlite.Statement{
		{SQL: `CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB, error_details BLOB);`},
		{SQL: `CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, size INTEGER, data BLOB);`},
		{SQL: `INSERT INTO steps (idx, step_type, status, metadata) VALUES (0, 14, 3, ?)`, Args: []any{metaUser}},
		{SQL: `INSERT INTO steps (idx, step_type, status, metadata) VALUES (1, 15, 3, ?)`, Args: []any{metaUser}},
		{SQL: `INSERT INTO gen_metadata (idx, size, data) VALUES (0, ?, ?)`, Args: []any{len(top), top}},
	})
	if err != nil {
		t.Fatalf("seed db: %v", err)
	}
	_ = db.Close()

	engine := &MetricsEngine{
		conversationsDir: tmp,
		summariesDbPath:  filepath.Join(tmp, "conversation_summaries.db"),
		cache:            make(map[string]*cachedDbState),
	}
	bridge := NewBridge(engine)
	defer bridge.Stop()

	var mu sync.Mutex
	var pushed string
	done := make(chan struct{}, 1)
	bridge.AttachCodexTarget("page-1", func(_ context.Context, expr string) (bool, error) {
		mu.Lock()
		pushed = expr
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
		return true, nil
	}, nil)

	bridge.HandleRequestMetrics("page-1", convID)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for codex telemetry push")
	}

	mu.Lock()
	got := pushed
	mu.Unlock()

	if !strings.Contains(got, "__ag_render_metrics") || !strings.Contains(got, "250 tok/s") || !strings.Contains(got, "Cache 90.0%") {
		t.Fatalf("unexpected pushed codex metrics expr: %s", got)
	}
	if !strings.Contains(got, "本轮最新调用") || !strings.Contains(got, "Gemini 4 Carbon (Medium)") {
		t.Fatalf("expected bento tooltip and model name in payload: %s", got)
	}
}

func TestLiveCodexMetricsIfPresent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	convDir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	entries, err := os.ReadDir(convDir)
	if err != nil {
		return
	}
	var latestID string
	var latestMod time.Time
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".db") {
			continue
		}
		cid := strings.TrimSuffix(name, ".db")
		if !codexUUIDRe.MatchString(cid) {
			continue
		}
		info, err := ent.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
			latestID = cid
		}
	}
	if latestID == "" {
		return
	}
	engine := NewMetricsEngine("")
	p := engine.ParseConversationCodexMetrics(context.Background(), latestID, CodexTelemetryOptions{
		TelemetryLayout:    "compact",
		TelemetryCacheMode: "session",
		LsLogFile:          "",
	})
	if p == nil || p.TransientError {
		t.Fatalf("unexpected parse failure on live db %s: %+v", latestID, p)
	}
	t.Logf("Live Codex Metrics (%s): hasData=%v turns=%d", latestID, p.HasData, len(p.Turns))
	if p.HasData && p.Stats != nil {
		t.Logf("  model=%q tps=%d ctx=%d/%d (%d%%) cache=%s",
			p.Stats.LastModel, p.Stats.Tps, p.Stats.Ctx, p.Stats.ContextLimit, p.Stats.CtxPct, p.Stats.SessionCacheRate)
	}
}
