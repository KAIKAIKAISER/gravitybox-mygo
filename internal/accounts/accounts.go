package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/win32"
)

const (
	keyOAuthToken = "antigravityUnifiedStateSync.oauthToken"
	keyUserStatus = "antigravityUnifiedStateSync.userStatus"
)

// SavedAccount mirrors GravityBox's persisted account summary & metadata.
type SavedAccount struct {
	ID               string `json:"id"`
	Email            string `json:"email"`
	Name             string `json:"name,omitempty"`
	SubscriptionTier string `json:"subscription_tier,omitempty"`
	LastUsed         int64  `json:"last_used,omitempty"`
	Disabled         bool   `json:"disabled,omitempty"`
}

type accountIndexFile struct {
	CurrentAccountID string         `json:"current_account_id"`
	Accounts         []SavedAccount `json:"accounts"`
}

// StateDBPath returns %APPDATA%\Antigravity\User\globalStorage\state.vscdb.
func StateDBPath() string {
	return filepath.Join(config.AntigravityUserDataDir(), "User", "globalStorage", "state.vscdb")
}

// ListSaved loads the GravityBox account index from %APPDATA%\GravityBox\accounts.json.
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
	sort.Slice(idx.Accounts, func(i, j int) bool {
		return idx.Accounts[i].LastUsed > idx.Accounts[j].LastUsed
	})
	return idx.CurrentAccountID, idx.Accounts, nil
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

	// Persist encrypted credential payload in Windows Credential Manager (never plaintext tokens in JSON).
	secretBytes, _ := json.Marshal(kv)
	if err := win32.CredWrite("gravitybox.vscdb", email, string(secretBytes)); err != nil {
		return fmt.Errorf("写入 Windows 凭据管理器失败: %w", err)
	}

	_, list, _ := ListSaved()
	now := time.Now().Unix()
	updated := false
	for i := range list {
		if list[i].Email == email {
			list[i].Name = name
			list[i].SubscriptionTier = tier
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
	return saveIndex(email, list)
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
