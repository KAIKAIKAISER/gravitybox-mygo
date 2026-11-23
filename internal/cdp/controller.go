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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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

// CodexSettings configures Antigravity Codex theme & real-time telemetry injection.
type CodexSettings struct {
	ThemeEnabled       bool
	ThemePreset        string // "auto", "codex-pure-white", "codex-dark"
	TelemetryLayout    string // "compact", "detailed"
	TelemetryCacheMode string // "session", "step"
	ManualContextLimit int
	ThemeVars          map[string]string
}

// HudHandler coordinates bi-directional runtime HUD metrics over CDP bindings.
type HudHandler interface {
	AttachTarget(targetID string, push func(ctx context.Context, expr string) (bool, error))
	AttachCodexTarget(targetID string, pushMain func(ctx context.Context, expr string) (bool, error), evalMain func(ctx context.Context, expr string) (string, error))
	DetachTarget(targetID string)
	HandleBindingPayload(targetID, rawPayload string)
	HandleRequestMetrics(targetID, rawPayload string)
	RefreshAll()
	RefreshTarget(targetID string)
}

type attachedSession struct {
	targetID        string
	client          *Client
	isSwarm         bool
	docScriptID     string
	docMainScriptID string

	mu        sync.Mutex
	url       string
	contextID *int64
}

// Controller manages CDP connections and injects runtime localization + Codex theme & HUD scripts into Antigravity windows.
type Controller struct {
	ctrlCtx    context.Context
	ctrlCancel context.CancelFunc

	mu                 sync.Mutex
	debugPort          int
	locale             string
	translationEnabled bool
	hudEnabled         bool
	deepAsarActive     bool
	codexCfg           CodexSettings
	codexPatchVersion  string
	hudHandler         HudHandler

	bundleJS      string
	runtimeID     string
	codexBundleJS string
	codexFullCSS  string
	codexHudCSS   string
	dictJSON      json.RawMessage
	dictVer       string
	dictCount     int
	sessions      map[string]*attachedSession
	hostThemeMode map[string]string
	lastStatus    ControllerStatus
}

// NewController constructs a CDP injection controller backed by embedded runtime scripts, Codex theme/telemetry assets, and base dictionary.
func NewController(debugPort int, locale string, translationEnabled, hudEnabled bool) (*Controller, error) {
	bundle, err := buildRuntimeBundle()
	if err != nil {
		return nil, err
	}
	codexBundle, codexFullCSS, codexHudCSS, err := buildCodexBundle()
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

	defaultCodex := CodexSettings{
		ThemeEnabled:       true,
		ThemePreset:        "auto",
		TelemetryLayout:    "compact",
		TelemetryCacheMode: "session",
	}

	sum := sha256.Sum256([]byte(bundle))
	ctrlCtx, ctrlCancel := context.WithCancel(context.Background())
	ctrl := &Controller{
		ctrlCtx:            ctrlCtx,
		ctrlCancel:         ctrlCancel,
		debugPort:          debugPort,
		locale:             locale,
		translationEnabled: translationEnabled,
		hudEnabled:         hudEnabled,
		codexCfg:           defaultCodex,
		bundleJS:           bundle,
		runtimeID:          hex.EncodeToString(sum[:]),
		codexBundleJS:      codexBundle,
		codexFullCSS:       codexFullCSS,
		codexHudCSS:        codexHudCSS,
		dictJSON:           dictPayload,
		dictVer:            "base",
		dictCount:          len(entries),
		sessions:           make(map[string]*attachedSession),
		hostThemeMode:      make(map[string]string),
		lastStatus: ControllerStatus{
			Mode:              "waiting",
			DictionaryVersion: "base",
			DictionaryEntries: len(entries),
		},
	}
	ctrl.codexPatchVersion = ctrl.computeCodexPatchVersionLocked()
	return ctrl, nil
}

// SetHudHandler registers the live HUD metrics bridge with the CDP controller.
func (c *Controller) SetHudHandler(h HudHandler) {
	c.mu.Lock()
	c.hudHandler = h
	c.mu.Unlock()
}

// UpdateConfig updates runtime preferences and propagates them on the next sync.
func (c *Controller) UpdateConfig(debugPort int, locale string, translationEnabled, hudEnabled, deepAsarActive bool) {
	c.mu.Lock()
	localeChanged := c.locale != locale
	if debugPort > 0 {
		c.debugPort = debugPort
	}
	c.locale = locale
	c.translationEnabled = translationEnabled
	c.hudEnabled = hudEnabled
	c.deepAsarActive = deepAsarActive
	c.codexPatchVersion = c.computeCodexPatchVersionLocked()
	h := c.hudHandler
	c.mu.Unlock()

	if localeChanged && h != nil && hudEnabled {
		go h.RefreshAll()
	}
}

// UpdateCodexSettings updates Codex theme and real-time telemetry preferences.
func (c *Controller) UpdateCodexSettings(settings CodexSettings) {
	if settings.ThemePreset != "codex-pure-white" && settings.ThemePreset != "codex-dark" {
		settings.ThemePreset = "auto"
	}
	if settings.TelemetryLayout != "detailed" {
		settings.TelemetryLayout = "compact"
	}
	if settings.TelemetryCacheMode != "step" {
		settings.TelemetryCacheMode = "session"
	}
	c.mu.Lock()
	c.codexCfg = settings
	c.codexPatchVersion = c.computeCodexPatchVersionLocked()
	c.mu.Unlock()
}

func (c *Controller) computeCodexPatchVersionLocked() string {
	raw, _ := json.Marshal(struct {
		Hud   bool          `json:"h"`
		Codex CodexSettings `json:"c"`
		Hash  string        `json:"js"`
	}{
		Hud:   c.hudEnabled,
		Codex: c.codexCfg,
		Hash:  c.codexBundleJS[:min(128, len(c.codexBundleJS))],
	})
	h := sha256.Sum256(append(raw, []byte(c.codexFullCSS)...))
	ver := "gb-" + hex.EncodeToString(h[:8])
	if !c.hudEnabled && !c.codexCfg.ThemeEnabled {
		return ver + "-disabled"
	}
	return ver
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
	dormant := !c.translationEnabled && !c.hudEnabled && !c.codexCfg.ThemeEnabled
	c.mu.Unlock()

	if dormant || !appRunning {
		if dormant && appRunning {
			c.cleanupOpenSessions(ctx)
		}
		c.closeAllSessions()
		mode := "waiting"
		if dormant {
			mode = "paused"
		}
		return c.setStatus(mode, "", 0, nil)
	}

	permitted, ok := c.discoverPermittedTargets(ctx, port)
	if !ok {
		c.closeAllSessions()
		return c.setStatus("debug-port-missing", "", 0, nil)
	}

	permittedMap := make(map[string]Target, len(permitted))
	for _, t := range permitted {
		permittedMap[t.ID] = t
	}

	// Prune closed or disallowed sessions.
	var stale []*attachedSession
	c.mu.Lock()
	for id, sess := range c.sessions {
		if _, exists := permittedMap[id]; !exists {
			stale = append(stale, sess)
			delete(c.sessions, id)
		}
	}
	h := c.hudHandler
	c.mu.Unlock()
	for _, s := range stale {
		_ = s.client.Close()
		if h != nil {
			h.DetachTarget(s.targetID)
		}
	}

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

func (c *Controller) discoverPermittedTargets(ctx context.Context, port int) ([]Target, bool) {
	ports := []int{port}
	if activePort := readDevToolsActivePort(); activePort > 0 && activePort != port {
		ports = append(ports, activePort)
	}
	anyReachable := false
	for _, p := range ports {
		targets, err := ListTargets(ctx, p)
		if err != nil {
			continue
		}
		anyReachable = true
		permitted := make([]Target, 0, len(targets))
		for _, t := range targets {
			if isAllowedTarget(t) {
				permitted = append(permitted, t)
			}
		}
		if len(permitted) > 0 {
			return permitted, true
		}
	}
	return nil, anyReachable
}

func (c *Controller) ensureInjected(ctx context.Context, tgt Target) error {
	c.mu.Lock()
	sess := c.sessions[tgt.ID]
	hudHandler := c.hudHandler
	c.mu.Unlock()

	isSwarm := isSwarmIframe(tgt)

	if sess == nil {
		cli, err := Dial(c.ctrlCtx, tgt.WebSocketDebuggerURL)
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
		if !isSwarm {
			_, _ = cli.Call(ctx, "Runtime.addBinding", map[string]any{
				"name":                 "__gravityboxHudBridge",
				"executionContextName": isolatedWorldName,
			})
			_, _ = cli.Call(ctx, "Runtime.addBinding", map[string]any{
				"name": "__agRequestMetrics",
			})
			_, _ = cli.Call(ctx, "Runtime.addBinding", map[string]any{
				"name": "__agSyncHostThemeMode",
			})
		}
		sess = &attachedSession{
			targetID: tgt.ID,
			isSwarm:  isSwarm,
			url:      tgt.URL,
			client:   cli,
		}
		c.mu.Lock()
		c.sessions[tgt.ID] = sess
		c.mu.Unlock()
		go c.runSessionEvents(sess)
	}

	c.mu.Lock()
	hudEnabled := c.hudEnabled
	transEnabled := c.translationEnabled && !c.deepAsarActive
	locale := c.locale
	runtimeID := c.runtimeID
	codexCfg := c.codexCfg
	codexVer := c.codexPatchVersion
	c.mu.Unlock()

	// 1. Isolated World Localization Engine (main page targets only)
	if !isSwarm {
		sess.mu.Lock()
		ctxID := sess.contextID
		sessURL := sess.url
		sess.mu.Unlock()

		if transEnabled {
			if ctxID == nil {
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
				idCopy := world.ExecutionContextID
				sess.mu.Lock()
				sess.contextID = &idCopy
				if tree.FrameTree.Frame.URL != "" {
					sess.url = tree.FrameTree.Frame.URL
				}
				ctxID = sess.contextID
				sessURL = sess.url
				sess.mu.Unlock()
			}

			rawState, err := sess.client.Evaluate(ctx, `globalThis.__GRAVITYBOX_ENGINE_V1__?.state?.()`, ctxID)
			if err != nil {
				sess.mu.Lock()
				sess.contextID = nil
				sess.mu.Unlock()
			} else {
				var stateVal struct {
					Result struct {
						Value struct {
							Active    bool   `json:"active"`
							RuntimeID string `json:"runtimeId"`
						} `json:"value"`
					} `json:"result"`
				}
				_ = json.Unmarshal(rawState, &stateVal)
				if !stateVal.Result.Value.Active || stateVal.Result.Value.RuntimeID != runtimeID {
					bootExpr, berr := c.buildBootExpression(sessURL, locale, true, false)
					if berr == nil {
						if _, err := sess.client.Evaluate(ctx, bootExpr, ctxID); err == nil {
							c.registerOnNewDocument(ctx, sess, bootExpr)
						}
					}
				}
			}
		} else if ctxID != nil {
			_, _ = sess.client.Evaluate(ctx, `globalThis.__GRAVITYBOX_ENGINE_V1__?.dispose?.({keepHud:true})`, ctxID)
			c.removeOnNewDocument(ctx, sess)
		}
	}

	// 2. Main World Codex Theme & Live Telemetry Injection
	if !isSwarm && hudHandler != nil {
		hudHandler.AttachCodexTarget(tgt.ID, c.makeMainPushFunc(sess), c.makeMainEvalStringFunc(sess))
	}

	codexActive := hudEnabled || codexCfg.ThemeEnabled
	if isSwarm && !codexCfg.ThemeEnabled {
		codexActive = false
	}

	rawMainState, err := sess.client.Evaluate(ctx, `(() => ({
		ver: window.__ag_patch_version || '',
		ready: window.__ag_body_ready === true,
		hasStyle: !!document.getElementById('ag-codex-theme')
	}))()`, nil)
	if err != nil {
		return err
	}
	var mainState struct {
		Result struct {
			Value struct {
				Ver      string `json:"ver"`
				Ready    bool   `json:"ready"`
				HasStyle bool   `json:"hasStyle"`
			} `json:"value"`
		} `json:"result"`
	}
	_ = json.Unmarshal(rawMainState, &mainState)

	needsMainInject := false
	if codexActive {
		needsMainInject = mainState.Result.Value.Ver != codexVer || !mainState.Result.Value.Ready || !mainState.Result.Value.HasStyle
	} else if mainState.Result.Value.Ver != "" && !strings.HasSuffix(mainState.Result.Value.Ver, "-disabled") {
		needsMainInject = true
	}

	if needsMainInject {
		if !isSwarm {
			_, _ = sess.client.Call(ctx, "Runtime.addBinding", map[string]any{
				"name": "__agRequestMetrics",
			})
			_, _ = sess.client.Call(ctx, "Runtime.addBinding", map[string]any{
				"name": "__agSyncHostThemeMode",
			})
		}
		mainExpr, err := c.buildCodexMainBootExpression(isSwarm, codexActive, hudEnabled, codexCfg, codexVer)
		if err != nil {
			return err
		}
		if _, err := sess.client.Evaluate(ctx, mainExpr, nil); err != nil {
			return err
		}
		if codexActive {
			c.registerMainOnNewDocument(ctx, sess, mainExpr)
		} else {
			c.removeMainOnNewDocument(ctx, sess)
		}
		if !isSwarm {
			modeRaw, merr := sess.client.Evaluate(ctx, `document.documentElement ? (document.documentElement.getAttribute('data-ag-theme-mode') || '') : ''`, nil)
			if merr == nil {
				var mv struct {
					Result struct {
						Value string `json:"value"`
					} `json:"result"`
				}
				if json.Unmarshal(modeRaw, &mv) == nil && (mv.Result.Value == "dark" || mv.Result.Value == "light") {
					c.mu.Lock()
					c.hostThemeMode[tgt.ID] = mv.Result.Value
					c.mu.Unlock()
				}
			}
			if hudEnabled && hudHandler != nil {
				go hudHandler.RefreshTarget(tgt.ID)
			}
		}
	}
	return nil
}

func (c *Controller) makeMainPushFunc(sess *attachedSession) func(context.Context, string) (bool, error) {
	return func(pctx context.Context, expr string) (bool, error) {
		callCtx, cancel := context.WithTimeout(pctx, 3*time.Second)
		defer cancel()
		raw, err := sess.client.Evaluate(callCtx, expr, nil)
		if err != nil {
			return false, err
		}
		var res struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &res) == nil {
			return res.Result.Value, nil
		}
		return true, nil
	}
}

func (c *Controller) makeMainEvalStringFunc(sess *attachedSession) func(context.Context, string) (string, error) {
	return func(pctx context.Context, expr string) (string, error) {
		callCtx, cancel := context.WithTimeout(pctx, 2*time.Second)
		defer cancel()
		raw, err := sess.client.Evaluate(callCtx, expr, nil)
		if err != nil {
			return "", err
		}
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &res) == nil {
			return res.Result.Value, nil
		}
		return "", nil
	}
}

func (c *Controller) removeOnNewDocument(ctx context.Context, sess *attachedSession) {
	sess.mu.Lock()
	oldID := sess.docScriptID
	sess.docScriptID = ""
	sess.mu.Unlock()
	if oldID != "" {
		_, _ = sess.client.Call(ctx, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{
			"identifier": oldID,
		})
	}
}

func (c *Controller) registerMainOnNewDocument(ctx context.Context, sess *attachedSession, source string) {
	c.removeMainOnNewDocument(ctx, sess)
	raw, err := sess.client.Call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": source,
	})
	if err != nil {
		return
	}
	var reg struct {
		Identifier string `json:"identifier"`
	}
	if json.Unmarshal(raw, &reg) == nil && reg.Identifier != "" {
		sess.mu.Lock()
		sess.docMainScriptID = reg.Identifier
		sess.mu.Unlock()
	}
}

func (c *Controller) removeMainOnNewDocument(ctx context.Context, sess *attachedSession) {
	sess.mu.Lock()
	oldID := sess.docMainScriptID
	sess.docMainScriptID = ""
	sess.mu.Unlock()
	if oldID != "" {
		_, _ = sess.client.Call(ctx, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{
			"identifier": oldID,
		})
	}
}

func (c *Controller) cleanupOpenSessions(ctx context.Context) {
	c.mu.Lock()
	open := make([]*attachedSession, 0, len(c.sessions))
	for _, s := range c.sessions {
		open = append(open, s)
	}
	codexCfg := c.codexCfg
	codexVer := c.codexPatchVersion
	c.mu.Unlock()

	for _, sess := range open {
		sess.mu.Lock()
		ctxID := sess.contextID
		sess.mu.Unlock()
		if ctxID != nil {
			_, _ = sess.client.Evaluate(ctx, `globalThis.__GRAVITYBOX_ENGINE_V1__?.dispose?.()`, ctxID)
		}
		c.removeOnNewDocument(ctx, sess)
		c.removeMainOnNewDocument(ctx, sess)
		if expr, err := c.buildCodexMainBootExpression(sess.isSwarm, false, false, codexCfg, codexVer); err == nil {
			_, _ = sess.client.Evaluate(ctx, expr, nil)
		}
	}
}

func (c *Controller) relaySwarmThemeMode(mode string) {
	if mode != "dark" && mode != "light" {
		return
	}
	c.mu.Lock()
	var targets []*attachedSession
	for _, s := range c.sessions {
		if s.isSwarm {
			targets = append(targets, s)
		}
	}
	c.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	quoted := strconvQuote(mode)
	expr := `window.__ag_host_theme_mode = ` + quoted + `; if (typeof window.__ag_set_host_theme_mode === 'function') window.__ag_set_host_theme_mode(` + quoted + `);`
	for _, s := range targets {
		ctx, cancel := context.WithTimeout(c.ctrlCtx, 2*time.Second)
		_, _ = s.client.Evaluate(ctx, expr, nil)
		cancel()
	}
}

func (c *Controller) registerOnNewDocument(ctx context.Context, sess *attachedSession, source string) {
	sess.mu.Lock()
	oldID := sess.docScriptID
	sess.mu.Unlock()
	if oldID != "" {
		_, _ = sess.client.Call(ctx, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{
			"identifier": oldID,
		})
	}
	raw, err := sess.client.Call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source":    source,
		"worldName": isolatedWorldName,
	})
	if err != nil {
		return
	}
	var reg struct {
		Identifier string `json:"identifier"`
	}
	if json.Unmarshal(raw, &reg) == nil && reg.Identifier != "" {
		sess.mu.Lock()
		sess.docScriptID = reg.Identifier
		sess.mu.Unlock()
	}
}

func (c *Controller) runSessionEvents(sess *attachedSession) {
	events := sess.client.Events()
	closed := sess.client.Closed()
	for {
		select {
		case <-c.ctrlCtx.Done():
			return
		case <-closed:
			c.mu.Lock()
			if cur, ok := c.sessions[sess.targetID]; ok && cur == sess {
				delete(c.sessions, sess.targetID)
			}
			h := c.hudHandler
			c.mu.Unlock()
			if h != nil {
				h.DetachTarget(sess.targetID)
			}
			return
		case ev := <-events:
			switch ev.Method {
			case "Runtime.bindingCalled":
				var p struct {
					Name               string `json:"name"`
					Payload            string `json:"payload"`
					ExecutionContextID int64  `json:"executionContextId"`
				}
				if json.Unmarshal(ev.Params, &p) == nil {
					switch p.Name {
					case "__gravityboxHudBridge":
						c.mu.Lock()
						h := c.hudHandler
						c.mu.Unlock()
						if h != nil {
							h.HandleBindingPayload(sess.targetID, p.Payload)
						}
					case "__agRequestMetrics":
						c.mu.Lock()
						h := c.hudHandler
						c.mu.Unlock()
						if h != nil {
							h.HandleRequestMetrics(sess.targetID, p.Payload)
						}
					case "__agSyncHostThemeMode":
						mode := strings.TrimSpace(p.Payload)
						if mode == "dark" || mode == "light" {
							c.mu.Lock()
							prev := c.hostThemeMode[sess.targetID]
							c.hostThemeMode[sess.targetID] = mode
							c.mu.Unlock()
							if prev != mode {
								go c.relaySwarmThemeMode(mode)
							}
						}
					}
				}
			case "Runtime.executionContextDestroyed":
				var p struct {
					ExecutionContextID int64 `json:"executionContextId"`
				}
				if json.Unmarshal(ev.Params, &p) == nil && p.ExecutionContextID != 0 {
					sess.mu.Lock()
					matched := sess.contextID != nil && *sess.contextID == p.ExecutionContextID
					if matched {
						sess.contextID = nil
					}
					sess.mu.Unlock()
					if matched {
						c.mu.Lock()
						h := c.hudHandler
						c.mu.Unlock()
						if h != nil {
							h.DetachTarget(sess.targetID)
						}
					}
				}
			case "Page.frameNavigated":
				var p struct {
					Frame struct {
						ParentID string `json:"parentId"`
						URL      string `json:"url"`
					} `json:"frame"`
				}
				if json.Unmarshal(ev.Params, &p) == nil && p.Frame.ParentID == "" {
					sess.mu.Lock()
					if p.Frame.URL != "" {
						sess.url = p.Frame.URL
					}
					sess.contextID = nil
					sess.mu.Unlock()
				}
			}
		}
	}
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

func (c *Controller) buildCodexMainBootExpression(isSwarm, codexActive, hudEnabled bool, cfg CodexSettings, patchVersion string) (string, error) {
	plat := runtime.GOOS
	if plat == "windows" {
		plat = "win32"
	}
	forceMode := ""
	preset := "codex-pure-white"
	switch cfg.ThemePreset {
	case "codex-pure-white":
		forceMode = "light"
		preset = "codex-pure-white"
	case "codex-dark":
		forceMode = "dark"
		preset = "codex-dark"
	}
	layout := cfg.TelemetryLayout
	if layout != "detailed" {
		layout = "compact"
	}
	cacheMode := cfg.TelemetryCacheMode
	if cacheMode != "step" {
		cacheMode = "session"
	}
	cssText := c.codexHudCSS
	if cfg.ThemeEnabled {
		cssText = c.codexFullCSS
	}

	bootObj := map[string]any{
		"version":            patchVersion,
		"enabled":            codexActive,
		"platform":           plat,
		"preset":             preset,
		"themeForceMode":     forceMode,
		"telemetryLayout":    layout,
		"telemetryCacheMode": cacheMode,
		"cssText":            cssText,
		"themeVars":          cfg.ThemeVars,
		"userConfig": map[string]any{
			"telemetryLayout":    layout,
			"telemetryCacheMode": cacheMode,
			"preset":             preset,
		},
		"modules": map[string]bool{
			"theme":             cfg.ThemeEnabled,
			"i18n":              false,
			"metrics":           hudEnabled && !isSwarm,
			"promptSuggestions": false,
			"deadlockHealer":    true,
			"zoomShortcuts":     true,
			"customCss":         false,
			"customJs":          false,
		},
	}
	bootBytes, err := json.Marshal(bootObj)
	if err != nil {
		return "", err
	}

	var preamble string
	if isSwarm {
		c.mu.Lock()
		for _, mode := range c.hostThemeMode {
			if mode == "dark" || mode == "light" {
				preamble = "window.__ag_host_theme_mode=" + strconvQuote(mode) + ";\n"
				break
			}
		}
		c.mu.Unlock()
	}

	return preamble + "var __AG_BOOT_PAYLOAD__=" + string(bootBytes) + ";\n" + c.codexBundleJS, nil
}

func (c *Controller) dropSession(id string) {
	c.mu.Lock()
	sess, ok := c.sessions[id]
	if ok {
		delete(c.sessions, id)
	}
	h := c.hudHandler
	c.mu.Unlock()
	if ok {
		_ = sess.client.Close()
		if h != nil {
			h.DetachTarget(id)
		}
	}
}

func (c *Controller) closeAllSessions() {
	c.mu.Lock()
	all := make([]*attachedSession, 0, len(c.sessions))
	for id, sess := range c.sessions {
		all = append(all, sess)
		delete(c.sessions, id)
	}
	h := c.hudHandler
	c.mu.Unlock()
	for _, s := range all {
		_ = s.client.Close()
		if h != nil {
			h.DetachTarget(s.targetID)
		}
	}
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

// Stop closes all CDP WebSocket sessions and stops background event listeners.
func (c *Controller) Stop() {
	c.ctrlCancel()
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

func buildCodexBundle() (bundleJS, fullCSS, hudCSS string, err error) {
	var styleParts []string
	var hudOnly string
	for _, name := range []string{
		"codex/styles/00-base.css",
		"codex/styles/10-light-codex.css",
		"codex/styles/20-dark-codex.css",
		"codex/styles/30-telemetry.css",
	} {
		raw, rerr := resources.ReadFile(name)
		if rerr != nil {
			return "", "", "", rerr
		}
		styleParts = append(styleParts, string(raw))
		if name == "codex/styles/30-telemetry.css" {
			hudOnly = string(raw)
		}
	}

	var jsParts []string
	for _, name := range []string{
		"codex/runtime/00-head.js",
		"codex/runtime/10-guards.js",
		"codex/runtime/20-theme-sync.js",
		"codex/runtime/30-antigravity-probe.js",
		"codex/runtime/40-status-machine.js",
		"codex/runtime/50-telemetry-ui.js",
		"codex/runtime/90-tail.js",
	} {
		raw, rerr := resources.ReadFile(name)
		if rerr != nil {
			return "", "", "", rerr
		}
		jsParts = append(jsParts, string(raw))
	}

	return strings.Join(jsParts, "\n\n"), strings.Join(styleParts, "\n"), hudOnly, nil
}

func stripESM(code string) string {
	out := importLineRe.ReplaceAllString(code, "")
	out = exportDeclRe.ReplaceAllString(out, "$1")
	out = exportNamedRe.ReplaceAllString(out, "")
	return out
}

func isSwarmIframe(t Target) bool {
	return t.Type == "iframe" && t.WebSocketDebuggerURL != "" && strings.HasPrefix(t.URL, "plugin://swarm--")
}

func isAllowedTarget(t Target) bool {
	if isSwarmIframe(t) {
		return true
	}
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
