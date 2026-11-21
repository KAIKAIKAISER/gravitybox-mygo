package quota

import (
	"context"
	"testing"
	"time"
)

func TestParseQuotaSummaryAndNormalization(t *testing.T) {
	raw := []byte(`{
		"response": {
			"groups": [
				{
					"displayName": "Gemini Models",
					"buckets": [
						{"bucketId": "gemini-5h", "window": "5h", "remainingFraction": 0.89, "resetTime": "2026-11-21T07:00:00Z"},
						{"bucketId": "gemini-weekly", "window": "weekly", "remainingFraction": 0.61, "resetTime": "2026-11-25T11:33:00Z"}
					]
				},
				{
					"displayName": "Claude and GPT models",
					"buckets": [
						{"bucketId": "3p-5h", "window": "5h", "remainingFraction": 1.0, "resetTime": "2026-11-21T07:27:53Z"}
					]
				}
			]
		}
	}`)
	fams := parseQuotaSummary(raw)
	if len(fams) != 2 {
		t.Fatalf("expected 2 families, got %d", len(fams))
	}
	wins := buildQuotaWindows(fams)
	if len(wins) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(wins))
	}
	if wins[0].Label != "Gemini · 5 小时" || wins[2].Label != "Claude/GPT · 5 小时" {
		t.Fatalf("unexpected window labels: %+v", wins)
	}
}

func TestLiveDaemonHubProbeIfRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	st := ProbeLive(ctx)
	t.Logf("ProbeLive: connected=%v email=%q tier=%q windows=%d err=%q", st.Connected, st.Email, st.Tier, len(st.Windows), st.Error)
}
