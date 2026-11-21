package accounts

import (
	"encoding/json"
	"testing"
)

func TestEnrichAccountQuotaGroupsParsing(t *testing.T) {
	raw := []byte(`{
		"id": "test-hermetic-account-id",
		"email": "user@example.com",
		"name": "Tester",
		"quota_last_updated": 1795088404,
		"quota": {
			"subscription_tier": "PRO",
			"last_updated": 1795088404,
			"quota_groups": [
				{
					"display_name": "Gemini Models",
					"buckets": [
						{"bucket_id": "gemini-5h", "window": "five_hour", "remaining_fraction": 0.9, "percentage": 90, "reset_time": "2026-11-25T11:33:00Z"},
						{"bucket_id": "gemini-weekly", "window": "weekly", "percentage": 45, "reset_time": "2026-11-26T11:33:00Z"}
					]
				}
			]
		}
	}`)
	var acc SavedAccount
	if err := json.Unmarshal(raw, &acc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	enrichAccountFromDisk(&acc)
	if !acc.HasQuota || len(acc.Windows) != 2 {
		t.Fatalf("expected 2 parsed quota windows from quota_groups, got hasQuota=%v len=%d", acc.HasQuota, len(acc.Windows))
	}
	if acc.SubscriptionTier != "PRO" || acc.MinRemainingPct != 45 {
		t.Fatalf("unexpected tier/minPct: tier=%q min=%v", acc.SubscriptionTier, acc.MinRemainingPct)
	}
	if acc.Windows[0].Label != "Gemini · 5 小时" || acc.Windows[1].Label != "Gemini · 每周" {
		t.Fatalf("unexpected labels: %+v", acc.Windows)
	}
}
