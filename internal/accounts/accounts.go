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

// SavedAccount mirrors GravityBox's persisted account summary & cached quota windows.
type SavedAccount struct {
	ID               string              `json:"id"`
	Email            string              `json:"email"`
	Name             string              `json:"name,omitempty"`
	SubscriptionTier string              `json:"subscription_tier,omitempty"`
	LastUsed         int64               `json:"last_used,omitempty"`
	UpdatedAt        int64               `json:"updated_at,omitempty"`
	Disabled         bool                `json:"disabled,omitempty"`
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
			Name  string `json:"name"`
			Quota *struct {
				LastUpdated      int64  `json:"last_updated"`
				SubscriptionTier string `json:"subscription_tier"`
				Groups           []struct {
					DisplayName string `json:"display_name"`
					Buckets     []struct {
						Window            string  `json:"window"`
						RemainingFraction float64 `json:"remaining_fraction"`
						ResetTime         string  `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"quota"`
		}
		if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		if acc.Name == "" && detail.Name != "" {
			acc.Name = detail.Name
		}
		if detail.Quota != nil {
			if acc.SubscriptionTier == "" && detail.Quota.SubscriptionTier != "" {
				acc.SubscriptionTier = detail.Quota.SubscriptionTier
			}
			if detail.Quota.LastUpdated > 0 {
				acc.UpdatedAt = detail.Quota.LastUpdated
			}
			minPct := 100.0
			for _, g := range detail.Quota.Groups {
				icon := "gemini"
				low := strings.ToLower(g.DisplayName)
				if strings.Contains(low, "claude") || strings.Contains(low, "gpt") {
					icon = "claude"
				} else if strings.Contains(low, "openai") {
					icon = "openai"
				}
				for _, b := range g.Buckets {
					pct := b.RemainingFraction * 100
					if pct < 0 {
						pct = 0
					} else if pct > 100 {
						pct = 100
					}
					if pct < minPct {
						minPct = pct
					}
					winLabel := g.DisplayName
					switch strings.ToLower(b.Window) {
					case "5h", "five_hour":
						winLabel += " · 5 小时"
					case "weekly", "7d":
						winLabel += " · 每周"
					}
					acc.Windows = append(acc.Windows, quota.QuotaWindow{
						Label:          winLabel,
						IconKey:        icon,
						RemainingPct:   pct,
						ResetCountdown: quota.FormatCountdown(b.ResetTime),
					})
					acc.HasQuota = true
				}
			}
			if acc.HasQuota {
				acc.MinRemainingPct = minPct
			}
		}
		break
	}
}

// SnapshotCurrentFromVscdb saves the currently active Antigravity state.vscdb auth rows as a switchable profile.
func SnapshotCurrentFromVscdb(ctx context.Context, email, name, tier string) error {
	if email == "" {
		return errors.New("未检测到当前登录邮箱，无法保存账号快照")
	}
	dbPath := StateDBPath()
	db, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(ctx,
		`SELECT key, value FROM ItemTable WHERE key IN (?, ?)`,
		keyOAuthToken, keyUserStatus,
	)
	if err != nil {
		return fmt.Errorf("读取登录状态失败: %w", err)
	}
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
	if len(kv) == 0 {
		return errors.New("state.vscdb 中未找到有效认证记录")
	}

	secretBytes, _ := json.Marshal(kv)
	if err := win32.CredWrite("gravitybox.vscdb", email, string(secretBytes)); err != nil {
		return fmt.Errorf("写入 Windows 凭据管理器失败: %w", err)
	}

	curID, list, _ := ListSaved()
	now := time.Now().Unix()
	updated := false
	for i := range list {
		if list[i].Email == email {
			if name != "" {
				list[i].Name = name
			}
			if tier != "" {
				list[i].SubscriptionTier = tier
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
		})
	}
	if curID == "" {
		curID = email
	}
	return saveIndex(email, list)
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
