package hud

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

func encodeVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func pbVarintField(fn int, v uint64) []byte {
	out := encodeVarint(uint64(fn<<3 | 0))
	return append(out, encodeVarint(v)...)
}

func pbBytesField(fn int, data []byte) []byte {
	out := encodeVarint(uint64(fn<<3 | 2))
	out = append(out, encodeVarint(uint64(len(data)))...)
	return append(out, data...)
}

func TestResolveModelCapacityAndPricing(t *testing.T) {
	spec := ResolveModelCapacity("models/gemini-4-carbon (Thinking)")
	if spec.ModelID != "gemini-4-carbon" || spec.CapacityTokens != 200_000 || spec.ShortLabel != "Carbon" {
		t.Fatalf("unexpected carbon spec: %+v", spec)
	}
	if _, _, _, _, ok := LookupPrice("gemini-4-carbon", float64(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC).Unix())); !ok {
		t.Fatal("expected pricing entry for gemini-4-carbon")
	}
}

func TestProtobufStepAndGenDecoding(t *testing.T) {
	tsStart := append(pbVarintField(1, 1763500000), pbVarintField(2, 0)...)
	tsEnd := append(pbVarintField(1, 1763500010), pbVarintField(2, 500_000_000)...)
	usage := append(pbVarintField(2, 1200), pbVarintField(3, 450)...)
	usage = append(usage, pbVarintField(5, 8800)...)
	modelInfo := append(pbVarintField(4, 200_000), pbBytesField(20, []byte("Gemini 4 Carbon"))...)

	meta := pbBytesField(1, tsStart)
	meta = append(meta, pbBytesField(7, tsEnd)...)
	meta = append(meta, pbBytesField(9, usage)...)
	meta = append(meta, pbBytesField(24, modelInfo)...)

	step := ParseStepMetadataBlob(1, 15, 3, meta, nil, nil, nil)
	if !step.HasUsage || step.RawInputTokens != 1200 || step.CachedReadTokens != 8800 || step.OutputTokens != 450 {
		t.Fatalf("unexpected decoded usage: %+v", step)
	}
	if step.ModelMaxTokens != 200_000 || step.ModelName != "Gemini 4 Carbon" {
		t.Fatalf("unexpected model info: %+v", step)
	}
	if step.StartTimeSeconds == nil || step.EndTimeSeconds == nil || *step.EndTimeSeconds-*step.StartTimeSeconds < 10.4 {
		t.Fatalf("unexpected step timestamps: start=%v end=%v", step.StartTimeSeconds, step.EndTimeSeconds)
	}

	// Ensure unused helper binary import isn't flagged if omitted.
	_ = binary.LittleEndian
}

func TestMetricsEngineAndBridgeIntegration(t *testing.T) {
	tmp := t.TempDir()
	convID := "11111111-1111-1111-1111-111111111111"
	dbPath := filepath.Join(tmp, convID+".db")

	ctx := context.Background()
	db, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Turn start step (step_type = 14)
	t0 := append(pbVarintField(1, 1763500000), pbVarintField(2, 0)...)
	metaTurn := pbBytesField(1, t0)

	// Model step (step_type = 15)
	t1 := append(pbVarintField(1, 1763500005), pbVarintField(2, 0)...)
	usage := append(pbVarintField(2, 2000), pbVarintField(3, 500)...)
	usage = append(usage, pbVarintField(5, 18000)...)
	minfo := append(pbVarintField(4, 200_000), pbBytesField(8, []byte("gemini-4-carbon"))...)
	metaModel := append(pbBytesField(1, t0), pbBytesField(7, t1)...)
	metaModel = append(metaModel, pbBytesField(9, usage)...)
	metaModel = append(metaModel, pbBytesField(24, minfo)...)

	_, err = db.Transaction(ctx, []sqlite.Statement{
		{SQL: `CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER, status INTEGER, metadata BLOB);`},
		{SQL: `INSERT INTO steps (idx, step_type, status, metadata) VALUES (0, 14, 3, ?)`, Args: []any{metaTurn}},
		{SQL: `INSERT INTO steps (idx, step_type, status, metadata) VALUES (1, 15, 3, ?)`, Args: []any{metaModel}},
	})
	if err != nil {
		t.Fatalf("seed steps: %v", err)
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
	var pushedExpr string
	done := make(chan struct{}, 1)
	bridge.AttachTarget("target-1", func(_ context.Context, expr string) (bool, error) {
		mu.Lock()
		pushedExpr = expr
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
		return true, nil
	})

	ev, _ := json.Marshal(ClientEvent{
		Type:            "SESSION_ATTACHED",
		ConversationID:  convID,
		CascadeID:       "slot-1",
		PaneSlotID:      "slot-1",
		SessionRevision: 1,
		ModelID:         "gemini-4-carbon",
	})
	bridge.HandleBindingPayload("target-1", string(ev))

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for HUD metrics push")
	}

	mu.Lock()
	got := pushedExpr
	mu.Unlock()

	if !strings.Contains(got, "__gravityboxUpdateHudMetrics") {
		t.Fatalf("expected push to call __gravityboxUpdateHudMetrics, got: %s", got)
	}
	if !strings.Contains(got, `"totalTokens":20500`) || !strings.Contains(got, `"turns":1`) || !strings.Contains(got, `"usedTokens":20000`) {
		t.Fatalf("unexpected pushed metrics payload: %s", got)
	}
}
