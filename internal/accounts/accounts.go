package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/quota"
	"gravitybox-mygo/internal/win32"
)

const (
	keyOAuthToken = "antigravityUnifiedStateSync.oauthToken"
	keyUserStatus = "antigravityUnifiedStateSync.userStatus"
)

type quotaBucketFile struct {
	BucketID          string   `json:"bucket_id,omitempty"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	Percentage        *float64 `json:"percentage,omitempty"`
	ResetTime         string   `json:"reset_time,omitempty"`
}

type quotaGroupFile struct {
	DisplayName string            `json:"display_name"`
	Buckets     []quotaBucketFile `json:"buckets"`
}

type quotaBlob struct {
	LastUpdated      int64            `json:"last_updated,omitempty"`
	SubscriptionTier string           `json:"subscription_tier,omitempty"`
	Groups           []quotaGroupFile `json:"groups,omitempty"`
	QuotaGroups      []quotaGroupFile `json:"quota_groups,omitempty"`
}

// SavedAccount mirrors GravityBox's persisted account summary & cached quota windows.
type SavedAccount struct {
	ID               string              `json:"id"`
	Email            string              `json:"email"`
	Name             string              `json:"name,omitempty"`
	SubscriptionTier string              `json:"subscription_tier,omitempty"`
	LastUsed         int64               `json:"last_used,omitempty"`
	UpdatedAt        int64               `json:"updated_at,omitempty"`
	QuotaLastUpdated int64               `json:"quota_last_updated,omitempty"`
	Disabled         bool                `json:"disabled,omitempty"`
	Quota            *quotaBlob          `json:"quota,omitempty"`
	Windows          []quota.QuotaWindow `json:"-"`
	MinRemainingPct  float64             `json:"-"`
	HasQuota         bool                `json:"-"`
}

// AutoSwitchConfig stores smart-switch preferences in %APPDATA%\GravityBox\auto_switch.json.
type AutoSwitchConfig struct {
	Enabled      bool    `json:"enabled"`
	ThresholdPct float64 `json:"thresholdPct"`
	TargetPct    float64 `json:"targetPct"`
}

type accountIndexFile struct {
	Version          int            `json:"version,omitempty"`
	CurrentAccountID string         `json:"current_account_id"`
	Accounts         []SavedAccount `json:"accounts"`
}

// StateDBPath returns %APPDATA%\Antigravity\User\globalStorage\state.vscdb.
func StateDBPath() string {
	return filepath.Join(config.AntigravityUserDataDir(), "User", "globalStorage", "state.vscdb")
}

// ListSaved loads the GravityBox account index and cached quota files from %APPDATA%\GravityBox\.
func ListSaved() (currentID string, accounts []SavedAccount, err error) {
	indexPath := filepath.Join(config.StateDir(), "accounts.json")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, nil
		}
		return "", nil, err
	}
	var idx accountIndexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		return "", nil, err
	}
	for i := range idx.Accounts {
		enrichAccountFromDisk(&idx.Accounts[i])
	}
	sort.Slice(idx.Accounts, func(i, j int) bool {
		return idx.Accounts[i].LastUsed > idx.Accounts[j].LastUsed
	})
	return idx.CurrentAccountID, idx.Accounts, nil
}

func enrichAccountFromDisk(acc *SavedAccount) {
	if acc.UpdatedAt == 0 && acc.QuotaLastUpdated > 0 {
		acc.UpdatedAt = acc.QuotaLastUpdated
	}
	if acc.Quota != nil {
		applyQuotaBlob(acc, acc.Quota)
	}
	candidates := []string{
		filepath.Join(config.StateDir(), "accounts", acc.ID+".json"),
		filepath.Join(config.StateDir(), "accounts", acc.Email+".json"),
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var detail struct {
			Name  string     `json:"name"`
			Quota *quotaBlob `json:"quota"`
		}
		if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		if acc.Name == "" && detail.Name != "" {
			acc.Name = detail.Name
		}
		if detail.Quota != nil {
			if !acc.HasQuota || detail.Quota.LastUpdated >= acc.UpdatedAt {
				applyQuotaBlob(acc, detail.Quota)
			}
		}
		break
	}
}

func applyQuotaBlob(acc *SavedAccount, q *quotaBlob) {
	if q == nil {
		return
	}
	if acc.SubscriptionTier == "" && q.SubscriptionTier != "" {
		acc.SubscriptionTier = q.SubscriptionTier
	} else if q.SubscriptionTier != "" {
		acc.SubscriptionTier = q.SubscriptionTier
	}
	if q.LastUpdated > acc.UpdatedAt {
		acc.UpdatedAt = q.LastUpdated
	}
	groups := q.QuotaGroups
	if len(groups) == 0 {
		groups = q.Groups
	}
	if len(groups) == 0 {
		return
	}

	var windows []quota.QuotaWindow
	minPct := 100.0
	for _, g := range groups {
		gName := quota.NormalizeFamilyName(g.DisplayName)
		icon := "gemini"
		low := strings.ToLower(gName)
		if strings.Contains(low, "claude") || strings.Contains(low, "gpt") {
			icon = "claude"
		} else if strings.Contains(low, "openai") {
			icon = "openai"
		}
		for _, b := range g.Buckets {
			var pct float64
			hasVal := false
			if b.RemainingFraction != nil {
				pct = *b.RemainingFraction * 100
				hasVal = true
			} else if b.Percentage != nil {
				pct = *b.Percentage
				hasVal = true
			}
			if !hasVal {
				continue
			}
			if pct < 0 {
				pct = 0
			} else if pct > 100 {
				pct = 100
			}
			if pct < minPct {
				minPct = pct
			}
			winLabel := gName
			switch strings.ToLower(strings.TrimSpace(b.Window)) {
			case "5h", "five_hour", "five-hour":
				winLabel += " · 5 小时"
			case "weekly", "7d", "seven_day":
				winLabel += " · 每周"
			}
			windows = append(windows, quota.QuotaWindow{
				Label:          winLabel,
				IconKey:        icon,
				RemainingPct:   pct,
				ResetCountdown: quota.FormatCountdown(b.ResetTime),
			})
		}
	}
	if len(windows) > 0 {
		acc.Windows = windows
		acc.MinRemainingPct = minPct
		acc.HasQuota = true
	}
}

// SnapshotCurrentFromVscdb saves the currently active Antigravity account profile (and state.vscdb tokens when available).
func SnapshotCurrentFromVscdb(ctx context.Context, email, name, tier string, families ...quota.FamilyQuota) error {
	if email == "" {
		return errors.New("未检测到当前登录邮箱，无法保存账号快照")
	}
	dbPath := StateDBPath()
	if _, err := os.Stat(dbPath); err == nil {
		if db, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{ReadOnly: true}); err == nil {
			defer db.Close()
			if rows, err := db.Query(ctx,
				`SELECT key, value FROM ItemTable WHERE key IN (?, ?)`,
				keyOAuthToken, keyUserStatus,
			); err == nil {
				kv := make(map[string]string)
				for _, r := range rows.Values {
					if len(r) >= 2 {
						k, _ := r[0].(string)
						v, _ := r[1].(string)
						if k != "" && v != "" {
							kv[k] = v
						}
					}
				}
				if len(kv) > 0 {
					secretBytes, _ := json.Marshal(kv)
					_ = win32.CredWrite("gravitybox.vscdb", email, string(secretBytes))
				}
			}
		}
	}

	_, list, _ := ListSaved()
	now := time.Now().Unix()
	qb := buildQuotaBlob(tier, now, families)
	updated := false
	targetID := email
	for i := range list {
		if strings.EqualFold(list[i].Email, email) {
			targetID = list[i].ID
			if name != "" {
				list[i].Name = name
			}
			if tier != "" {
				list[i].SubscriptionTier = tier
			}
			if qb != nil {
				list[i].Quota = qb
				list[i].QuotaLastUpdated = now
				list[i].UpdatedAt = now
			}
			list[i].LastUsed = now
			updated = true
			break
		}
	}
	if !updated {
		list = append(list, SavedAccount{
			ID:               email,
			Email:            email,
			Name:             name,
			SubscriptionTier: tier,
			LastUsed:         now,
			UpdatedAt:        now,
			QuotaLastUpdated: now,
			Quota:            qb,
		})
	}
	if qb != nil {
		_ = writeAccountDetailFile(targetID, email, name, qb)
	}
	return saveIndex(targetID, list)
}

func buildQuotaBlob(tier string, now int64, families []quota.FamilyQuota) *quotaBlob {
	if len(families) == 0 && tier == "" {
		return nil
	}
	qb := &quotaBlob{
		LastUpdated:      now,
		SubscriptionTier: tier,
	}
	for _, f := range families {
		g := quotaGroupFile{DisplayName: quota.NormalizeFamilyName(f.Family)}
		if f.HasFiveHour {
			frac := f.FiveHourPct / 100.0
			pct := f.FiveHourPct
			g.Buckets = append(g.Buckets, quotaBucketFile{
				Window:            "5h",
				RemainingFraction: &frac,
				Percentage:        &pct,
				ResetTime:         f.FiveHourReset,
			})
		}
		if f.HasWeekly {
			frac := f.WeeklyPct / 100.0
			pct := f.WeeklyPct
			g.Buckets = append(g.Buckets, quotaBucketFile{
				Window:            "weekly",
				RemainingFraction: &frac,
				Percentage:        &pct,
				ResetTime:         f.WeeklyReset,
			})
		}
		if !f.HasFiveHour && !f.HasWeekly {
			frac := f.RemainingPct / 100.0
			pct := f.RemainingPct
			g.Buckets = append(g.Buckets, quotaBucketFile{
				RemainingFraction: &frac,
				Percentage:        &pct,
			})
		}
		qb.QuotaGroups = append(qb.QuotaGroups, g)
	}
	return qb
}

func writeAccountDetailFile(id, email, name string, qb *quotaBlob) error {
	dir := filepath.Join(config.StateDir(), "accounts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	payload := map[string]any{
		"id":                 id,
		"email":              email,
		"name":               name,
		"quota":              qb,
		"quota_last_updated": qb.LastUpdated,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, id+".json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, id+".json"))
}

// RenameAccount updates the display name of a saved account.
func RenameAccount(email, newName string) error {
	curID, list, err := ListSaved()
	if err != nil {
		return err
	}
	newName = strings.TrimSpace(newName)
	for i := range list {
		if list[i].Email == email || list[i].ID == email {
			list[i].Name = newName
			return saveIndex(curID, list)
		}
	}
	return errors.New("未找到指定账号")
}

// DeleteAccount removes a saved account from index and clears its credential snapshot.
func DeleteAccount(email string) error {
	curID, list, err := ListSaved()
	if err != nil {
		return err
	}
	filtered := make([]SavedAccount, 0, len(list))
	for _, acc := range list {
		if acc.Email == email || acc.ID == email {
			_ = win32.CredDelete("gravitybox.vscdb", acc.Email)
			_ = os.Remove(filepath.Join(config.StateDir(), "accounts", acc.ID+".json"))
			continue
		}
		filtered = append(filtered, acc)
	}
	if curID == email {
		curID = ""
	}
	return saveIndex(curID, filtered)
}

// SwitchAccount performs an atomic Windows account switch via state.vscdb transaction + Antigravity relaunch.
func SwitchAccount(ctx context.Context, email, appDir string, debugPort int) error {
	if _, err := os.Stat(StateDBPath()); err != nil {
		return errors.New("当前 Antigravity 版本未使用 state.vscdb 存储登录态，请在 Antigravity 内切换账号后刷新")
	}
	secretRaw, found, err := win32.CredRead("gravitybox.vscdb", email)
	if err != nil {
		return err
	}
	if !found || secretRaw == "" {
		return fmt.Errorf("未在 Windows 凭据管理器中找到 %s 的凭证快照，请先在该账号登录时保存一次快照", email)
	}
	var kv map[string]string
	if err := json.Unmarshal([]byte(secretRaw), &kv); err != nil || len(kv) == 0 {
		return errors.New("凭证快照格式损坏")
	}

	if win32.IsAntigravityRunning() {
		if err := win32.StopAntigravityGracefully(ctx); err != nil {
			return fmt.Errorf("关闭 Antigravity 失败: %w", err)
		}
	}

	db, err := sqlite.Open(ctx, StateDBPath(), sqlite.OpenOptions{})
	if err != nil {
		return fmt.Errorf("打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()

	stmts := []sqlite.Statement{
		{SQL: `DELETE FROM ItemTable WHERE key IN (?, ?)`, Args: []any{keyOAuthToken, keyUserStatus}},
	}
	for k, v := range kv {
		stmts = append(stmts, sqlite.Statement{
			SQL:  `INSERT INTO ItemTable(key, value) VALUES(?, ?)`,
			Args: []any{k, v},
		})
	}
	if _, err := db.Transaction(ctx, stmts); err != nil {
		return fmt.Errorf("写入 state.vscdb 事务失败: %w", err)
	}

	_, list, _ := ListSaved()
	for i := range list {
		if list[i].Email == email {
			list[i].LastUsed = time.Now().Unix()
		}
	}
	_ = saveIndex(email, list)

	if appDir != "" {
		return win32.LaunchAntigravity(filepath.Join(appDir, "Antigravity.exe"), debugPort)
	}
	return nil
}

// LoadAutoSwitch reads auto_switch.json from %APPDATA%\GravityBox.
func LoadAutoSwitch() AutoSwitchConfig {
	cfg := AutoSwitchConfig{Enabled: false, ThresholdPct: 10, TargetPct: 30}
	raw, err := os.ReadFile(filepath.Join(config.StateDir(), "auto_switch.json"))
	if err != nil {
		return cfg
	}
	var parsed AutoSwitchConfig
	if json.Unmarshal(raw, &parsed) == nil {
		if parsed.ThresholdPct > 0 {
			cfg.ThresholdPct = parsed.ThresholdPct
		}
		if parsed.TargetPct > 0 {
			cfg.TargetPct = parsed.TargetPct
		}
		cfg.Enabled = parsed.Enabled
	}
	return cfg
}

// SaveAutoSwitch saves auto_switch.json atomically.
func SaveAutoSwitch(cfg AutoSwitchConfig) error {
	dir := config.StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "auto_switch.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "auto_switch.json"))
}

func saveIndex(currentID string, accounts []SavedAccount) error {
	dir := config.StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(accountIndexFile{
		CurrentAccountID: currentID,
		Accounts:         accounts,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "accounts.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "accounts.json"))
}
