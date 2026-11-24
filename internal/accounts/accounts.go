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
	keyOAuthToken    = "antigravityUnifiedStateSync.oauthToken"
	keyUserStatus    = "antigravityUnifiedStateSync.userStatus"
	credServiceLive  = "gemini"
	credAccountLive  = "antigravity"
	credServiceAuth2 = "gravitybox.auth2"
	credServiceVscdb = "gravitybox.vscdb"
)

var (
	credReadFn             = win32.CredRead
	credWriteFn            = win32.CredWrite
	credDeleteFn           = win32.CredDelete
	isAntigravityRunningFn = win32.IsAntigravityRunning
	stopAntigravityFn      = win32.StopAntigravityGracefully
	launchAntigravityFn    = win32.LaunchAntigravity
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
	ID                    string              `json:"id"`
	Email                 string              `json:"email"`
	Name                  string              `json:"name,omitempty"`
	SubscriptionTier      string              `json:"subscription_tier,omitempty"`
	LastUsed              int64               `json:"last_used,omitempty"`
	UpdatedAt             int64               `json:"updated_at,omitempty"`
	QuotaLastUpdated      int64               `json:"quota_last_updated,omitempty"`
	Disabled              bool                `json:"disabled,omitempty"`
	Quota                 *quotaBlob          `json:"quota,omitempty"`
	Windows               []quota.QuotaWindow `json:"-"`
	MinRemainingPct       float64             `json:"-"`
	HasQuota              bool                `json:"-"`
	HasCredentialSnapshot bool                `json:"-"`
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

// AppStoragePath returns %APPDATA%\Antigravity\app_storage.json (used by Antigravity 2.34.0+).
func AppStoragePath() string {
	return filepath.Join(config.AntigravityUserDataDir(), "app_storage.json")
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
		idx.Accounts[i].HasCredentialSnapshot = hasCredentialSnapshot(&idx.Accounts[i])
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

type storedOAuthToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

type storedTokenRecord struct {
	Token        *storedOAuthToken `json:"token"`
	AuthMethod   string            `json:"auth_method,omitempty"`
	IDTokenEmail string            `json:"id_token_email,omitempty"`
	WriterID     string            `json:"writer_id,omitempty"`
}

func hasCredentialSnapshot(acc *SavedAccount) bool {
	if acc == nil || acc.Email == "" {
		return false
	}
	if raw, ok, err := credReadFn(credServiceAuth2, acc.Email); err == nil && ok && strings.TrimSpace(raw) != "" {
		return true
	}
	if raw, ok, err := credReadFn(credServiceVscdb, acc.Email); err == nil && ok && strings.TrimSpace(raw) != "" {
		return true
	}
	if raw, err := readLegacyTokenAsStoredToken(acc.ID, acc.Email); err == nil && raw != "" {
		return true
	}
	return false
}

func readLegacyTokenAsStoredToken(id, email string) (string, error) {
	var candidates []string
	if id != "" {
		candidates = append(candidates, filepath.Join(config.StateDir(), "accounts", id+".json"))
	}
	if email != "" && email != id {
		candidates = append(candidates, filepath.Join(config.StateDir(), "accounts", email+".json"))
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var legacy struct {
			Email string `json:"email"`
			Token *struct {
				AccessToken     string `json:"access_token"`
				RefreshToken    string `json:"refresh_token"`
				ExpiresIn       int64  `json:"expires_in"`
				ExpiryTimestamp int64  `json:"expiry_timestamp"`
				Expiry          string `json:"expiry"`
				TokenType       string `json:"token_type"`
				Email           string `json:"email"`
			} `json:"token"`
		}
		if json.Unmarshal(raw, &legacy) != nil || legacy.Token == nil || legacy.Token.RefreshToken == "" {
			continue
		}
		tokEmail := strings.TrimSpace(legacy.Token.Email)
		if tokEmail == "" {
			tokEmail = strings.TrimSpace(legacy.Email)
		}
		if tokEmail == "" {
			tokEmail = email
		}
		expiry := strings.TrimSpace(legacy.Token.Expiry)
		if expiry == "" && legacy.Token.ExpiryTimestamp > 0 {
			expiry = time.Unix(legacy.Token.ExpiryTimestamp, 0).UTC().Format(time.RFC3339)
		}
		tt := strings.TrimSpace(legacy.Token.TokenType)
		if tt == "" {
			tt = "Bearer"
		}
		rec := storedTokenRecord{
			Token: &storedOAuthToken{
				AccessToken:  legacy.Token.AccessToken,
				TokenType:    tt,
				RefreshToken: legacy.Token.RefreshToken,
				Expiry:       expiry,
				ExpiresIn:    legacy.Token.ExpiresIn,
			},
			AuthMethod:   "consumer",
			IDTokenEmail: tokEmail,
		}
		b, err := json.Marshal(rec)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", errors.New("no legacy token")
}

func updateAppStorageUsername(email string) {
	p := AppStoragePath()
	raw, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return
	}
	m["agy.onboarding.lastLoginUsername"] = email
	out, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, out, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// SnapshotCurrentFromVscdb saves the currently active Antigravity account profile and login credentials
// (supporting Antigravity 2.34.0+ Windows Credential Manager gemini:antigravity as well as legacy state.vscdb).
func SnapshotCurrentFromVscdb(ctx context.Context, email, name, tier string, families ...quota.FamilyQuota) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return errors.New("未检测到当前登录邮箱，无法保存账号快照")
	}

	_, list, err := ListSaved()
	if err != nil {
		return fmt.Errorf("读取已保存账号索引失败，已中止覆盖保存: %w", err)
	}

	savedCred := false
	var credErr error

	// 1. Antigravity 2.34.0+: live OAuth token stored in Credential Manager "gemini:antigravity".
	if raw, found, rErr := credReadFn(credServiceLive, credAccountLive); rErr != nil {
		credErr = rErr
	} else if found && strings.TrimSpace(raw) != "" {
		var rec struct {
			IDTokenEmail string `json:"id_token_email"`
			Token        *struct {
				Email        string `json:"email"`
				RefreshToken string `json:"refresh_token"`
				AccessToken  string `json:"access_token"`
			} `json:"token"`
		}
		if json.Unmarshal([]byte(raw), &rec) == nil && rec.Token != nil && (rec.Token.RefreshToken != "" || rec.Token.AccessToken != "") {
			credEmail := strings.TrimSpace(rec.IDTokenEmail)
			if credEmail == "" {
				credEmail = strings.TrimSpace(rec.Token.Email)
			}
			if credEmail != "" && !strings.EqualFold(credEmail, email) {
				return fmt.Errorf("当前凭据属于 %s，与界面检测到的 %s 不一致，请刷新配额后再保存", credEmail, email)
			} else if wErr := credWriteFn(credServiceAuth2, email, raw); wErr != nil {
				credErr = wErr
			} else {
				savedCred = true
			}
		}
	}

	// 2. Legacy VS Code Antigravity: ItemTable inside state.vscdb.
	dbPath := StateDBPath()
	if _, statErr := os.Stat(dbPath); statErr == nil {
		if db, oErr := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{ReadOnly: true}); oErr == nil {
			defer db.Close()
			if rows, qErr := db.Query(ctx,
				`SELECT key, value FROM ItemTable WHERE key IN (?, ?)`,
				keyOAuthToken, keyUserStatus,
			); qErr == nil {
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
					if wErr := credWriteFn(credServiceVscdb, email, string(secretBytes)); wErr != nil {
						if credErr == nil {
							credErr = wErr
						}
					} else {
						savedCred = true
					}
				}
			}
		}
	}

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
	if err := saveIndex(targetID, list); err != nil {
		return err
	}
	if !savedCred {
		if credErr != nil {
			return fmt.Errorf("已保存账号资料，但登录凭据快照保存失败: %w", credErr)
		}
		return errors.New("已保存账号资料，但未在系统凭据管理器或 state.vscdb 中找到登录凭据，请先在 Antigravity 内重新登录")
	}
	return nil
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
		if strings.EqualFold(acc.Email, email) || acc.ID == email {
			_ = credDeleteFn(credServiceAuth2, acc.Email)
			_ = credDeleteFn(credServiceVscdb, acc.Email)
			_ = os.Remove(filepath.Join(config.StateDir(), "accounts", acc.ID+".json"))
			_ = os.Remove(filepath.Join(config.StateDir(), "accounts", acc.Email+".json"))
			continue
		}
		filtered = append(filtered, acc)
	}
	if strings.EqualFold(curID, email) {
		curID = ""
	}
	return saveIndex(curID, filtered)
}

// SwitchAccount performs an atomic Windows account switch via Antigravity 2.34.0 credential store (or legacy state.vscdb) + relaunch.
func SwitchAccount(ctx context.Context, email, appDir string, debugPort int) error {
	curID, list, err := ListSaved()
	if err != nil {
		return err
	}
	var targetID string
	for _, acc := range list {
		if strings.EqualFold(acc.Email, email) || acc.ID == email {
			targetID = acc.ID
			break
		}
	}

	// Prefer Antigravity 2.34.0 StoredToken snapshot (or legacy Electron account token converted cleanly).
	auth2Raw, auth2Found, auth2Err := credReadFn(credServiceAuth2, email)
	if !auth2Found || strings.TrimSpace(auth2Raw) == "" {
		if legacyRaw, lErr := readLegacyTokenAsStoredToken(targetID, email); lErr == nil && legacyRaw != "" {
			auth2Raw = legacyRaw
			auth2Found = true
			auth2Err = nil
		}
	}
	if auth2Found && strings.TrimSpace(auth2Raw) != "" {
		if isAntigravityRunningFn() {
			if err := stopAntigravityFn(ctx); err != nil {
				return fmt.Errorf("关闭 Antigravity 失败: %w", err)
			}
		}
		if err := credWriteFn(credServiceLive, credAccountLive, auth2Raw); err != nil {
			return fmt.Errorf("写入 Antigravity 登录凭据失败: %w", err)
		}
		updateAppStorageUsername(email)

		newCurrentID := targetID
		if newCurrentID == "" {
			newCurrentID = email
		}
		for i := range list {
			if strings.EqualFold(list[i].Email, email) || list[i].ID == targetID {
				list[i].LastUsed = time.Now().Unix()
			}
		}
		_ = saveIndex(newCurrentID, list)
		if appDir != "" {
			return launchAntigravityFn(filepath.Join(appDir, "Antigravity.exe"), debugPort)
		}
		return nil
	}
	if _, err := os.Stat(StateDBPath()); err != nil {
		if auth2Err != nil {
			return auth2Err
		}
		return fmt.Errorf("未在系统凭据管理器中找到 %s 的登录凭据快照，请先在该账号登录时点击「保存当前登录账号」", email)
	}

	secretRaw, found, err := credReadFn(credServiceVscdb, email)
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

	if isAntigravityRunningFn() {
		if err := stopAntigravityFn(ctx); err != nil {
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

	newCurrentID := targetID
	if newCurrentID == "" {
		newCurrentID = curID
	}
	if newCurrentID == "" {
		newCurrentID = email
	}
	for i := range list {
		if strings.EqualFold(list[i].Email, email) {
			list[i].LastUsed = time.Now().Unix()
		}
	}
	_ = saveIndex(newCurrentID, list)

	if appDir != "" {
		return launchAntigravityFn(filepath.Join(appDir, "Antigravity.exe"), debugPort)
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
