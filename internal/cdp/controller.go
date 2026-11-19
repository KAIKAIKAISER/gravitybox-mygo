package cdp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/resources"
)

const isolatedWorldName = "GRAVITYBOX-localization-v1"

var (
	importLineRe  = regexp.MustCompile(`(?m)^\s*import\s+[^;]*?from\s+['"][^'"]+['"];?\s*$`)
	exportDeclRe  = regexp.MustCompile(`(?m)^\s*export\s+((?:async\s+function|const|let|var|function|class)\s+)`)
	exportNamedRe = regexp.MustCompile(`(?m)^\s*export\s*\{[^}]*\};?\s*$`)
	allowedPathRe = regexp.MustCompile(`(?i)^/(?:history|sidecars|customizations|automations|settings|settings-standalone|terminal-standalone|new-project|multi|artifact|s|chatski-connect)(?:/.*)?$|^/c(?:/|/(?:[0-9a-f-]{36}|_new)(?:/.*)?)?$|^/(?:forked|view|battle)/.*$`)
)

// ControllerStatus reports live CDP injection state to the Native UI.
type ControllerStatus struct {
	Mode               string
	Windows            int
	DictionaryVersion  string
	DictionaryEntries  int
	LastError          string
	ConnectedTargetIDs []string
}

type attachedSession struct {
	targetID  string
	url       string
	client    *Client
	contextID *int64
}

// Controller manages CDP connections and injects runtime localization + HUD scripts into Antigravity windows.
type Controller struct {
	mu                 sync.Mutex
	debugPort          int
	locale             string
	translationEnabled bool
	hudEnabled         bool
	deepAsarActive     bool

	bundleJS   string
	runtimeID  string
	dictJSON   json.RawMessage
	dictVer    string
	dictCount  int
	sessions   map[string]*attachedSession
	lastStatus ControllerStatus
}

// NewController constructs a CDP injection controller backed by embedded runtime scripts and base dictionary.
func NewController(debugPort int, locale string, translationEnabled, hudEnabled bool) (*Controller, error) {
	bundle, err := buildRuntimeBundle()
	if err != nil {
		return nil, err
	}
	dictRaw, err := resources.ReadFile("dict/base.json")
	if err != nil {
		return nil, err
	}
	var entries map[string]string
	if err := json.Unmarshal(dictRaw, &entries); err != nil {
		return nil, err
	}
	dictPayload, err := json.Marshal(map[string]any{
		"schema":  1,
		"version": "base",
		"entries": entries,
	})
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(bundle))
	return &Controller{
		debugPort:          debugPort,
		locale:             locale,
		translationEnabled: translationEnabled,
		hudEnabled:         hudEnabled,
		bundleJS:           bundle,
		runtimeID:          hex.EncodeToString(sum[:]),
		dictJSON:           dictPayload,
		dictVer:            "base",
		dictCount:          len(entries),
		sessions:           make(map[string]*attachedSession),
		lastStatus: ControllerStatus{
			Mode:              "waiting",
			DictionaryVersion: "base",
			DictionaryEntries: len(entries),
		},
	}, nil
}

// UpdateConfig updates runtime preferences and propagates them on the next sync.
func (c *Controller) UpdateConfig(debugPort int, locale string, translationEnabled, hudEnabled, deepAsarActive bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if debugPort > 0 {
		c.debugPort = debugPort
	}
	c.locale = locale
	c.translationEnabled = translationEnabled
	c.hudEnabled = hudEnabled
	c.deepAsarActive = deepAsarActive
}

// Status returns a snapshot of the current CDP injection state.
func (c *Controller) Status() ControllerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStatus
}

// SyncOnce discovers loopback targets and ensures runtime/HUD scripts are injected into permitted Antigravity windows.
func (c *Controller) SyncOnce(ctx context.Context, appRunning bool) ControllerStatus {
	c.mu.Lock()
	port := c.debugPort
	dormant := !c.translationEnabled && !c.hudEnabled
	c.mu.Unlock()

	if dormant || !appRunning {
		c.closeAllSessions()
		mode := "waiting"
		if dormant {
			mode = "paused"
		}
		return c.setStatus(mode, "", 0, nil)
	}

	targets, err := ListTargets(ctx, port)
	if err != nil {
		if activePort := readDevToolsActivePort(); activePort > 0 && activePort != port {
			if t2, err2 := ListTargets(ctx, activePort); err2 == nil {
				targets = t2
				err = nil
			}
		}
	}
	if err != nil {
		c.closeAllSessions()
		return c.setStatus("debug-port-missing", "", 0, nil)
	}

	permitted := make([]Target, 0, len(targets))
	permittedMap := make(map[string]Target)
	for _, t := range targets {
		if isAllowedTarget(t) {
			permitted = append(permitted, t)
			permittedMap[t.ID] = t
		}
	}

	// Prune closed or disallowed sessions.
	c.mu.Lock()
	for id, sess := range c.sessions {
		if _, ok := permittedMap[id]; !ok {
			_ = sess.client.Close()
			delete(c.sessions, id)
		}
	}
	c.mu.Unlock()

	var lastErr string
	for _, tgt := range permitted {
		if err := c.ensureInjected(ctx, tgt); err != nil {
			lastErr = err.Error()
		}
	}

	c.mu.Lock()
	ids := make([]string, 0, len(c.sessions))
	for id := range c.sessions {
		ids = append(ids, id)
	}
	c.mu.Unlock()

	mode := "waiting-window"
	if len(ids) > 0 {
		mode = "connected"
	}
	return c.setStatus(mode, lastErr, len(ids), ids)
}

func (c *Controller) ensureInjected(ctx context.Context, tgt Target) error {
	c.mu.Lock()
	sess := c.sessions[tgt.ID]
	c.mu.Unlock()

	if sess == nil {
		cli, err := Dial(ctx, tgt.WebSocketDebuggerURL)
		if err != nil {
			return err
		}
		if _, err := cli.Call(ctx, "Page.enable", map[string]any{}); err != nil {
			_ = cli.Close()
			return err
		}
		if _, err := cli.Call(ctx, "Runtime.enable", map[string]any{}); err != nil {
			_ = cli.Close()
			return err
		}
		sess = &attachedSession{
			targetID: tgt.ID,
			url:      tgt.URL,
			client:   cli,
		}
		c.mu.Lock()
		c.sessions[tgt.ID] = sess
		c.mu.Unlock()
	}

	// Ensure isolated world exists and state matches current runtimeId.
	if sess.contextID == nil {
		rawTree, err := sess.client.Call(ctx, "Page.getFrameTree", map[string]any{})
		if err != nil {
			c.dropSession(tgt.ID)
			return err
		}
		var tree struct {
			FrameTree struct {
				Frame struct {
					ID  string `json:"id"`
					URL string `json:"url"`
				} `json:"frame"`
			} `json:"frameTree"`
		}
		if err := json.Unmarshal(rawTree, &tree); err != nil || tree.FrameTree.Frame.ID == "" {
			return err
		}
		rawWorld, err := sess.client.Call(ctx, "Page.createIsolatedWorld", map[string]any{
			"frameId":   tree.FrameTree.Frame.ID,
			"worldName": isolatedWorldName,
		})
		if err != nil {
			c.dropSession(tgt.ID)
			return err
		}
		var world struct {
			ExecutionContextID int64 `json:"executionContextId"`
		}
		if err := json.Unmarshal(rawWorld, &world); err != nil || world.ExecutionContextID == 0 {
			return err
		}
		sess.contextID = &world.ExecutionContextID
		if tree.FrameTree.Frame.URL != "" {
			sess.url = tree.FrameTree.Frame.URL
		}
	}

	// Check if runtime is already active in this context.
	rawState, err := sess.client.Evaluate(ctx, `globalThis.__GRAVITYBOX_ENGINE_V1__?.state?.()`, sess.contextID)
	if err != nil {
		sess.contextID = nil
		return err
	}
	var stateVal struct {
		Result struct {
			Value struct {
				Active    bool   `json:"active"`
				RuntimeID string `json:"runtimeId"`
				Version   string `json:"version"`
			} `json:"value"`
		} `json:"result"`
	}
	_ = json.Unmarshal(rawState, &stateVal)

	c.mu.Lock()
	hudEnabled := c.hudEnabled
	transEnabled := c.translationEnabled && !c.deepAsarActive
	locale := c.locale
	runtimeID := c.runtimeID
	c.mu.Unlock()

	if !stateVal.Result.Value.Active || stateVal.Result.Value.RuntimeID != runtimeID {
		bootExpr, err := c.buildBootExpression(sess.url, locale, transEnabled, hudEnabled)
		if err != nil {
			return err
		}
		if _, err := sess.client.Evaluate(ctx, bootExpr, sess.contextID); err != nil {
			return err
		}
	}

	// Sync HUD & language state.
	hudLocale := "zh-Hans"
	if locale == "en" {
		hudLocale = "en"
	}
	if hudEnabled {
		expr := `(globalThis.__GRAVITYBOX_ATTACH_HUD__?.(), globalThis.__GRAVITYBOX_HUD_V1__?.setLanguage?.(` +
			strconvQuote(hudLocale) + `), globalThis.__GRAVITYBOX_HUD_V1__?.setEnabled?.(true), globalThis.__GRAVITYBOX_HUD_V1__?.syncAllSessions?.())`
		_, _ = sess.client.Evaluate(ctx, expr, sess.contextID)
	} else {
		_, _ = sess.client.Evaluate(ctx, `globalThis.__GRAVITYBOX_HUD_V1__?.setEnabled?.(false)`, sess.contextID)
	}
	return nil
}

func (c *Controller) buildBootExpression(pageURL, locale string, transEnabled, hudEnabled bool) (string, error) {
	u, err := url.Parse(pageURL)
	if err != nil {
		return "", err
	}
	langMode := "zh"
	hudLocale := "zh-Hans"
	if !transEnabled || locale == "en" {
		langMode = "en"
	}
	if locale == "en" {
		hudLocale = "en"
	}
	bootObj := map[string]any{
		"origin":             u.Scheme + "://" + u.Host,
		"path":               u.Path,
		"dictionary":         c.dictJSON,
		"conservative":       true,
		"runtimeId":          c.runtimeID,
		"shortcutEnabled":    true,
		"hudEnabled":         hudEnabled,
		"translationEnabled": transEnabled,
		"languageMode":       langMode,
		"locale":             hudLocale,
		"shellValidated":     true,
	}
	bootBytes, err := json.Marshal(bootObj)
	if err != nil {
		return "", err
	}
	return "delete globalThis.__GRAVITYBOX_ATTACH_DEEP_SCAN__;delete globalThis.__GRAVITYBOX_ATTACH_HUD__;globalThis.__GRAVITYBOX_BOOT__=" +
		string(bootBytes) + ";\n" + c.bundleJS, nil
}

func (c *Controller) dropSession(id string) {
	c.mu.Lock()
	if sess, ok := c.sessions[id]; ok {
		_ = sess.client.Close()
		delete(c.sessions, id)
	}
	c.mu.Unlock()
}

func (c *Controller) closeAllSessions() {
	c.mu.Lock()
	for id, sess := range c.sessions {
		_ = sess.client.Close()
		delete(c.sessions, id)
	}
	c.mu.Unlock()
}

func (c *Controller) setStatus(mode, lastErr string, windows int, ids []string) ControllerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastStatus = ControllerStatus{
		Mode:               mode,
		Windows:            windows,
		DictionaryVersion:  c.dictVer,
		DictionaryEntries:  c.dictCount,
		LastError:          lastErr,
		ConnectedTargetIDs: ids,
	}
	return c.lastStatus
}

// Stop closes all CDP WebSocket sessions.
func (c *Controller) Stop() {
	c.closeAllSessions()
}

func buildRuntimeBundle() (string, error) {
	var baseParts []string
	for _, name := range []string{"runtime/engine.js", "runtime/deep-scan.js"} {
		raw, err := resources.ReadFile(name)
		if err != nil {
			return "", err
		}
		baseParts = append(baseParts, string(raw))
	}

	var hudParts []string
	for _, name := range []string{
		"runtime/metric-format.js",
		"runtime/hud-i18n.js",
		"runtime/hud-styles.js",
		"runtime/hud-popover.js",
		"runtime/hud-instance.js",
		"runtime/hud-manager.js",
	} {
		raw, err := resources.ReadFile(name)
		if err != nil {
			return "", err
		}
		hudParts = append(hudParts, stripESM(string(raw)))
	}

	hudBundle := "(function bootstrapGravityBoxHud() {\n'use strict';\n" +
		strings.Join(hudParts, "\n") + "\n})();"
	return strings.Join(append(baseParts, hudBundle), "\n"), nil
}

func stripESM(code string) string {
	out := importLineRe.ReplaceAllString(code, "")
	out = exportDeclRe.ReplaceAllString(out, "$1")
	out = exportNamedRe.ReplaceAllString(out, "")
	return out
}

func isAllowedTarget(t Target) bool {
	if t.Type != "page" || t.WebSocketDebuggerURL == "" {
		return false
	}
	u, err := url.Parse(t.URL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return false
	}
	return u.Path == "/" || allowedPathRe.MatchString(u.Path)
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func readDevToolsActivePort() int {
	p := filepath.Join(config.AntigravityUserDataDir(), "DevToolsActivePort")
	raw, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || port <= 0 || port >= 65536 {
		return 0
	}
	return port
}
