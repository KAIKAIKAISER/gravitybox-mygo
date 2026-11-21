package usage

import (
	"context"
	"testing"
	"time"
)

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
