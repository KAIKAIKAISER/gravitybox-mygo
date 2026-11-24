package accounts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/quota"
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

func TestSnapshotAndSwitchAntigravity234Credentials(t *testing.T) {
	tmp := t.TempDir()
	appData := filepath.Join(tmp, "roaming")
	t.Setenv("APPDATA", appData)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	stateDir := config.StateDir()
	agDir := config.AntigravityUserDataDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "accounts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agDir, "app_storage.json"), []byte(`{"agy.onboarding.lastLoginUsername":"alice@example.com","other":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate legacy Electron account file for bob@example.com
	if err := os.WriteFile(filepath.Join(stateDir, "accounts", "bob-id.json"), []byte(`{
		"id": "bob-id",
		"email": "bob@example.com",
		"name": "Bob",
		"token": {
			"access_token": "bob-access",
			"refresh_token": "bob-refresh",
			"expiry_timestamp": 1795088291,
			"expires_in": 3599,
			"token_type": "Bearer",
			"email": "bob@example.com"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveIndex("bob-id", []SavedAccount{{ID: "bob-id", Email: "bob@example.com", Name: "Bob"}}); err != nil {
		t.Fatal(err)
	}

	creds := map[string]string{
		credServiceLive + ":" + credAccountLive: `{"token":{"access_token":"alice-access","token_type":"Bearer","refresh_token":"alice-refresh","expiry":"2026-11-24T12:00:00Z","expires_in":3599},"auth_method":"consumer","id_token_email":"alice@example.com","writer_id":"w1"}`,
	}
	origRead, origWrite, origDel := credReadFn, credWriteFn, credDeleteFn
	origRun, origStop, origLaunch := isAntigravityRunningFn, stopAntigravityFn, launchAntigravityFn
	t.Cleanup(func() {
		credReadFn, credWriteFn, credDeleteFn = origRead, origWrite, origDel
		isAntigravityRunningFn, stopAntigravityFn, launchAntigravityFn = origRun, origStop, origLaunch
	})
	stopCalled := 0
	launchPath := ""
	isAntigravityRunningFn = func() bool { return true }
	stopAntigravityFn = func(context.Context) error { stopCalled++; return nil }
	launchAntigravityFn = func(p string, _ int) error { launchPath = p; return nil }
	credReadFn = func(service, account string) (string, bool, error) {
		v, ok := creds[service+":"+account]
		return v, ok, nil
	}
	credWriteFn = func(service, account, secret string) error {
		creds[service+":"+account] = secret
		return nil
	}
	credDeleteFn = func(service, account string) error {
		delete(creds, service+":"+account)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Email mismatch should error cleanly instead of saving wrong token.
	if err := SnapshotCurrentFromVscdb(ctx, "other@example.com", "Other", "PRO"); err == nil {
		t.Fatal("expected error when id_token_email does not match requested email")
	}

	// Snapshot Alice while Bob is already in accounts.json -> keeps Bob and saves Alice credential snapshot.
	err := SnapshotCurrentFromVscdb(ctx, "alice@example.com", "Alice", "PRO", quota.FamilyQuota{
		Family: "Gemini", HasFiveHour: true, FiveHourPct: 90,
	})
	if err != nil {
		t.Fatalf("SnapshotCurrentFromVscdb(alice): %v", err)
	}

	if got := creds[credServiceAuth2+":alice@example.com"]; !strings.Contains(got, "alice-refresh") {
		t.Fatalf("expected stored auth2 credential for alice, got %q", got)
	}

	curID, list, err := ListSaved()
	if err != nil {
		t.Fatalf("ListSaved: %v", err)
	}
	if curID != "alice@example.com" || len(list) != 2 {
		t.Fatalf("expected 2 preserved accounts with alice active, got curID=%q len=%d", curID, len(list))
	}
	for _, acc := range list {
		if !acc.HasCredentialSnapshot {
			t.Fatalf("expected HasCredentialSnapshot=true for %s", acc.Email)
		}
	}

	// Switch to Bob using converted legacy token JSON -> updates gemini:antigravity & app_storage.json.
	if err := SwitchAccount(ctx, "bob@example.com", "C:\\mock-app", 9222); err != nil {
		t.Fatalf("SwitchAccount(bob): %v", err)
	}
	if stopCalled != 1 || !strings.HasSuffix(launchPath, "Antigravity.exe") {
		t.Fatalf("expected stubbed stop/launch called once, got stop=%d launch=%q", stopCalled, launchPath)
	}
	liveCred := creds[credServiceLive+":"+credAccountLive]
	if !strings.Contains(liveCred, "bob-refresh") || !strings.Contains(liveCred, "bob@example.com") {
		t.Fatalf("expected live credential updated to bob, got %q", liveCred)
	}
	appStorageBytes, err := os.ReadFile(AppStoragePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(appStorageBytes), `"agy.onboarding.lastLoginUsername":"bob@example.com"`) {
		t.Fatalf("expected app_storage.json updated to bob, got %s", string(appStorageBytes))
	}

	// Corrupt accounts.json must abort SnapshotCurrentFromVscdb instead of overwriting saved index.
	if err := os.WriteFile(filepath.Join(stateDir, "accounts.json"), []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotCurrentFromVscdb(ctx, "alice@example.com", "Alice", "PRO"); err == nil {
		t.Fatal("expected SnapshotCurrentFromVscdb to abort when accounts.json is corrupt")
	}
}
