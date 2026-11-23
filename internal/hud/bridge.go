package hud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gravitybox-mygo/internal/config"
)

const (
	// BridgeBindingName is the legacy HUD bridge binding name kept for compatibility.
	BridgeBindingName     = "__gravityboxHudBridge"
	activityGraceWindowMs = 15_000
	codexMaxWatchedCids   = 6
)

// PushFunc evaluates an expression inside a CDP target's execution context.
type PushFunc = func(ctx context.Context, expr string) (bool, error)

// EvalStringFunc evaluates an expression returning a string value (used for Main World telemetry discovery).
type EvalStringFunc = func(ctx context.Context, expr string) (string, error)

// ClientEvent is the event JSON sent from legacy runtime/hud-instance.js via window.__gravityboxHudBridge.
type ClientEvent struct {
	Type                 string   `json:"type"`
	ConversationID       string   `json:"conversationId"`
	CascadeID            string   `json:"cascadeId"`
	PaneSlotID           string   `json:"paneSlotId"`
	SessionRevision      int64    `json:"sessionRevision"`
	ModelID              string   `json:"modelId"`
	IsStreaming          *bool    `json:"isStreaming"`
	ActivityPhase        string   `json:"activityPhase"`
	IsSubagentRunning    *bool    `json:"isSubagentRunning"`
	RunningSubagentCount *int     `json:"runningSubagentCount"`
	ChildConversationIDs []string `json:"childConversationIds"`
}

type activePane struct {
	targetID             string
	paneSlotID           string
	cascadeID            string
	conversationID       string
	sessionRevision      int64
	metricsSequence      int64
	modelID              string
	foregroundStreaming  bool
	isStreaming          bool
	activityPhase        string
	isSubagentRunning    bool
	runningSubagentCount int
	childConversationIDs []string
	lastActivityAtMs     int64
	lastFingerprint      string
}

type pushedSessionState struct {
	hash          string
	pendingActive bool
}

type codexTargetState struct {
	targetID       string
	pushMain       PushFunc
	evalMain       EvalStringFunc
	watchedIDs     []string
	pushedSessions map[string]pushedSessionState
	lastPollMs     int64
	lastHbPushMs   int64
}

// Bridge manages bidirectional HUD events & real-time Codex telemetry streaming across Antigravity panes.
type Bridge struct {
	engine  *MetricsEngine
	mu      sync.Mutex
	targets map[string]PushFunc
	panes   map[string]*activePane
	codex   map[string]*codexTargetState
	seq     atomic.Int64

	telemetryLayout    string
	telemetryCacheMode string
	manualContextLimit int64
	lsLogFile          string

	ctx    context.Context
	cancel context.CancelFunc
}

// NewBridge creates and starts a CdpHudBridge background polling loop.
func NewBridge(engine *MetricsEngine) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bridge{
		engine:             engine,
		targets:            make(map[string]PushFunc),
		panes:              make(map[string]*activePane),
		codex:              make(map[string]*codexTargetState),
		telemetryLayout:    "compact",
		telemetryCacheMode: "session",
		lsLogFile:          config.AntigravityLogFile(),
		ctx:                ctx,
		cancel:             cancel,
	}
	b.seq.Store(time.Now().UnixMilli())
	go b.pollLoop()
	return b
}

// Stop halts background polling.
func (b *Bridge) Stop() {
	b.cancel()
}

// UpdateTelemetryConfig updates live layout, cache rate mode, and manual context limit settings.
func (b *Bridge) UpdateTelemetryConfig(layout, cacheMode string, manualContextLimit int) {
	if layout != "detailed" {
		layout = "compact"
	}
	if cacheMode != "step" {
		cacheMode = "session"
	}
	if manualContextLimit < 0 {
		manualContextLimit = 0
	}

	b.mu.Lock()
	changed := b.telemetryLayout != layout ||
		b.telemetryCacheMode != cacheMode ||
		b.manualContextLimit != int64(manualContextLimit)
	b.telemetryLayout = layout
	b.telemetryCacheMode = cacheMode
	b.manualContextLimit = int64(manualContextLimit)
	if changed {
		for _, ts := range b.codex {
			ts.pushedSessions = make(map[string]pushedSessionState)
		}
	}
	b.mu.Unlock()

	if changed {
		go b.RefreshAll()
	}
}

// AttachTarget registers a CDP page evaluation callback for pushing legacy isolated-world HUD updates.
func (b *Bridge) AttachTarget(targetID string, push PushFunc) {
	b.mu.Lock()
	b.targets[targetID] = push
	prefix := targetID + "::"
	for k, p := range b.panes {
		if strings.HasPrefix(k, prefix) {
			p.lastFingerprint = ""
		}
	}
	b.mu.Unlock()
}

// AttachCodexTarget registers Main World evaluation callbacks for Codex telemetry pushing & discovery.
func (b *Bridge) AttachCodexTarget(targetID string, pushMain PushFunc, evalMain EvalStringFunc) {
	b.mu.Lock()
	ts := b.codex[targetID]
	if ts == nil {
		ts = &codexTargetState{
			targetID:       targetID,
			pushedSessions: make(map[string]pushedSessionState),
		}
		b.codex[targetID] = ts
	}
	ts.pushMain = pushMain
	ts.evalMain = evalMain
	ts.pushedSessions = make(map[string]pushedSessionState)
	b.mu.Unlock()
}

// DetachTarget removes a CDP target and its tracked panes.
func (b *Bridge) DetachTarget(targetID string) {
	b.mu.Lock()
	delete(b.targets, targetID)
	delete(b.codex, targetID)
	prefix := targetID + "::"
	for k := range b.panes {
		if strings.HasPrefix(k, prefix) {
			delete(b.panes, k)
		}
	}
	b.mu.Unlock()
}

// RefreshAll clears pane fingerprints & pushed session hashes and pushes fresh telemetry immediately.
func (b *Bridge) RefreshAll() {
	b.mu.Lock()
	paneKeys := make([]string, 0, len(b.panes))
	for k, p := range b.panes {
		p.lastFingerprint = ""
		paneKeys = append(paneKeys, k)
	}
	codexIDs := make([]string, 0, len(b.codex))
	for id, ts := range b.codex {
		ts.pushedSessions = make(map[string]pushedSessionState)
		codexIDs = append(codexIDs, id)
	}
	b.mu.Unlock()

	for _, k := range paneKeys {
		b.refreshPane(k, true)
	}
	for _, id := range codexIDs {
		b.syncCodexTarget(id, true)
	}
}

// RefreshTarget immediately syncs Codex telemetry for a single target (e.g. after Main World bundle injection).
func (b *Bridge) RefreshTarget(targetID string) {
	b.syncCodexTarget(targetID, true)
}

// HandleRequestMetrics processes window.__agRequestMetrics(payload) binding calls from 50-telemetry-ui.js.
func (b *Bridge) HandleRequestMetrics(targetID, rawPayload string) {
	watched := parseCascadeIDList(rawPayload)

	b.mu.Lock()
	ts := b.codex[targetID]
	if ts == nil {
		ts = &codexTargetState{
			targetID:       targetID,
			pushedSessions: make(map[string]pushedSessionState),
		}
		b.codex[targetID] = ts
	}
	ts.watchedIDs = watched
	watchedSet := make(map[string]bool, len(watched))
	for _, cid := range watched {
		watchedSet[cid] = true
		if prev, ok := ts.pushedSessions[cid]; ok {
			ts.pushedSessions[cid] = pushedSessionState{hash: "", pendingActive: prev.pendingActive}
		}
	}
	for k := range ts.pushedSessions {
		if !watchedSet[k] {
			delete(ts.pushedSessions, k)
		}
	}
	b.mu.Unlock()

	if len(watched) > 0 {
		go b.syncCodexTarget(targetID, true)
	}
}

// HandleBindingPayload handles Runtime.bindingCalled payloads for __gravityboxHudBridge.
func (b *Bridge) HandleBindingPayload(targetID, rawPayload string) {
	var ev ClientEvent
	if err := json.Unmarshal([]byte(rawPayload), &ev); err != nil {
		return
	}
	switch ev.Type {
	case "SESSION_ATTACHED", "MODEL_CHANGED", "STREAMING_CHANGED", "REQUEST_REFRESH", "SESSION_DETACHED":
	default:
		return
	}
	if ev.ConversationID == "" || ev.SessionRevision < 1 {
		return
	}
	cascadeID := ev.CascadeID
	if cascadeID == "" {
		cascadeID = "default-pane"
	}
	paneSlotID := ev.PaneSlotID
	if paneSlotID == "" {
		paneSlotID = cascadeID
	}
	paneKey := targetID + "::" + paneSlotID

	b.mu.Lock()
	existing := b.panes[paneKey]
	if existing != nil && (ev.SessionRevision < existing.sessionRevision ||
		(ev.SessionRevision == existing.sessionRevision && ev.ConversationID != existing.conversationID)) {
		b.mu.Unlock()
		return
	}

	if ev.Type == "SESSION_DETACHED" {
		if existing != nil && ev.SessionRevision == existing.sessionRevision && ev.ConversationID == existing.conversationID {
			delete(b.panes, paneKey)
			if paneSlotID == cascadeID {
				delete(b.panes, targetID+"::"+cascadeID)
			}
		}
		b.mu.Unlock()
		return
	}

	var sameConv *activePane
	if existing != nil && existing.conversationID == ev.ConversationID {
		sameConv = existing
	}

	isSubRun := false
	if ev.IsSubagentRunning != nil {
		isSubRun = *ev.IsSubagentRunning
	} else if sameConv != nil {
		isSubRun = sameConv.isSubagentRunning
	}

	runSubCnt := 0
	if ev.RunningSubagentCount != nil && *ev.RunningSubagentCount >= 0 {
		runSubCnt = *ev.RunningSubagentCount
	} else if isSubRun {
		runSubCnt = 1
		if sameConv != nil && sameConv.runningSubagentCount > 1 {
			runSubCnt = sameConv.runningSubagentCount
		}
	}

	childIDs := ev.ChildConversationIDs
	if childIDs == nil && sameConv != nil {
		childIDs = sameConv.childConversationIDs
	}

	fgStream := false
	if ev.IsStreaming != nil {
		fgStream = *ev.IsStreaming
	} else if sameConv != nil {
		fgStream = sameConv.foregroundStreaming
	}
	resolvedStream := fgStream || isSubRun

	phase := "idle"
	if fgStream {
		if ev.ActivityPhase == "executing" || ev.ActivityPhase == "generating" {
			phase = ev.ActivityPhase
		} else if sameConv != nil && sameConv.activityPhase != "" && sameConv.activityPhase != "idle" {
			phase = sameConv.activityPhase
		} else {
			phase = "generating"
		}
	}

	modelID := strings.TrimSpace(ev.ModelID)
	if modelID == "" && sameConv != nil {
		modelID = sameConv.modelID
	}

	nowMs := time.Now().UnixMilli()
	lastActivity := nowMs
	if sameConv != nil && !resolvedStream && ev.Type != "STREAMING_CHANGED" && !isSubRun {
		lastActivity = sameConv.lastActivityAtMs
	}
	lastFP := ""
	if sameConv != nil && ev.Type != "SESSION_ATTACHED" && ev.Type != "REQUEST_REFRESH" && ev.SessionRevision == sameConv.sessionRevision {
		lastFP = sameConv.lastFingerprint
	}
	var seq int64
	if sameConv != nil {
		seq = sameConv.metricsSequence
	}

	b.panes[paneKey] = &activePane{
		targetID:             targetID,
		paneSlotID:           paneSlotID,
		cascadeID:            cascadeID,
		conversationID:       ev.ConversationID,
		sessionRevision:      ev.SessionRevision,
		metricsSequence:      seq,
		modelID:              modelID,
		foregroundStreaming:  fgStream,
		isStreaming:          resolvedStream,
		activityPhase:        phase,
		isSubagentRunning:    isSubRun,
		runningSubagentCount: runSubCnt,
		childConversationIDs: childIDs,
		lastActivityAtMs:     lastActivity,
		lastFingerprint:      lastFP,
	}
	b.mu.Unlock()

	go b.refreshPane(paneKey, true)
}

func (b *Bridge) pollLoop() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastLegacyPollMs int64

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			nowMs := time.Now().UnixMilli()

			b.mu.Lock()
			legacyAnyActive := false
			legacyKeys := make([]string, 0, len(b.panes))
			for k, p := range b.panes {
				legacyKeys = append(legacyKeys, k)
				if p.isStreaming || p.isSubagentRunning || (nowMs-p.lastActivityAtMs < activityGraceWindowMs) {
					legacyAnyActive = true
				}
			}
			var dueCodexTargets []string
			for id, ts := range b.codex {
				if ts.pushMain == nil {
					continue
				}
				active := false
				for _, ps := range ts.pushedSessions {
					if ps.pendingActive {
						active = true
						break
					}
				}
				interval := int64(1000)
				if active {
					interval = 300
				}
				if nowMs-ts.lastPollMs >= interval-20 {
					ts.lastPollMs = nowMs
					dueCodexTargets = append(dueCodexTargets, id)
				}
			}
			b.mu.Unlock()

			if len(legacyKeys) > 0 {
				legacyInterval := int64(1000)
				if legacyAnyActive {
					legacyInterval = 300
				}
				if nowMs-lastLegacyPollMs >= legacyInterval-20 {
					lastLegacyPollMs = nowMs
					for _, k := range legacyKeys {
						b.refreshPane(k, false)
					}
				}
			}

			for _, id := range dueCodexTargets {
				b.syncCodexTarget(id, false)
			}
		}
	}
}

func (b *Bridge) syncCodexTarget(targetID string, force bool) {
	b.mu.Lock()
	ts := b.codex[targetID]
	if ts == nil || ts.pushMain == nil {
		b.mu.Unlock()
		return
	}
	pushMain := ts.pushMain
	evalMain := ts.evalMain
	watched := append([]string(nil), ts.watchedIDs...)
	layout := b.telemetryLayout
	cacheMode := b.telemetryCacheMode
	manualLim := b.manualContextLimit
	lsLogFile := b.lsLogFile
	lastHb := ts.lastHbPushMs
	b.mu.Unlock()

	if (len(watched) == 0 || force) && evalMain != nil {
		const discoverExpr = `(function(){
			if (typeof window.__ag_get_watched_cascade_ids === 'function') {
				const list = window.__ag_get_watched_cascade_ids();
				if (Array.isArray(list)) return JSON.stringify(list);
			}
			return '[]';
		})()`
		dCtx, dCancel := context.WithTimeout(b.ctx, 1500*time.Millisecond)
		rawList, err := evalMain(dCtx, discoverExpr)
		dCancel()
		if err == nil && rawList != "" {
			detected := parseCascadeIDList(rawList)
			if len(detected) > 0 || force {
				watched = detected
				b.mu.Lock()
				if cur := b.codex[targetID]; cur == ts {
					cur.watchedIDs = detected
					watchedSet := make(map[string]bool, len(detected))
					for _, cid := range detected {
						watchedSet[cid] = true
					}
					for k := range cur.pushedSessions {
						if !watchedSet[k] {
							delete(cur.pushedSessions, k)
						}
					}
				}
				b.mu.Unlock()
			}
		}
	}

	nowMs := time.Now().UnixMilli()
	pushedAny := false

	for _, cid := range watched {
		b.mu.Lock()
		cur := b.codex[targetID]
		if cur != ts {
			b.mu.Unlock()
			return
		}
		prev := cur.pushedSessions[cid]
		b.mu.Unlock()

		rawSig := b.engine.GetCodexDbSignature(cid)
		if rawSig == "" {
			rawSig = "empty"
		}
		lsSig := ""
		if prev.pendingActive && lsLogFile != "" {
			lsSig = GetLsLogSig(lsLogFile)
		}
		lsSuffix := ""
		if lsSig != "" {
			lsSuffix = "|ls:" + lsSig
		}
		sessionKey := fmt.Sprintf("%s@%s|lim:%d:256000|ly:%s:%s%s", cid, rawSig, manualLim, layout, cacheMode, lsSuffix)
		if !force && sessionKey == prev.hash {
			continue
		}

		payload := b.engine.ParseConversationCodexMetrics(b.ctx, cid, CodexTelemetryOptions{
			ManualContextLimit:   manualLim,
			FallbackContextLimit: 256000,
			TelemetryLayout:      layout,
			TelemetryCacheMode:   cacheMode,
			LsLogFile:            lsLogFile,
			LsSig:                lsSig,
		})
		if payload == nil {
			continue
		}
		toSend := payload
		if payload.TransientError {
			if !force || payload.CachedPayload == nil {
				continue
			}
			toSend = payload.CachedPayload
		}

		rawJSON, err := json.Marshal(toSend)
		if err != nil {
			continue
		}
		expr := `(window.__ag_last_daemon_hb = Date.now(), window.__ag_render_metrics && window.__ag_render_metrics(` + string(rawJSON) + `), true)`
		if ok, err := pushMain(b.ctx, expr); err != nil || !ok {
			continue
		}
		pushedAny = true

		if !payload.TransientError {
			nextPending := payload.Stats != nil && (payload.Stats.GeneratingPending || payload.Stats.CompactingNow || payload.Stats.PendingRetrying)
			b.mu.Lock()
			if cur := b.codex[targetID]; cur == ts {
				cur.pushedSessions[cid] = pushedSessionState{
					hash:          sessionKey,
					pendingActive: nextPending,
				}
				cur.lastHbPushMs = nowMs
			}
			b.mu.Unlock()
		}
	}

	if !pushedAny && nowMs-lastHb >= 10_000 {
		ok, err := pushMain(b.ctx, `((window.__ag_last_daemon_hb = Date.now()), window.__ag_refresh_wait_ui && window.__ag_refresh_wait_ui(), true)`)
		if err == nil && ok {
			b.mu.Lock()
			if cur := b.codex[targetID]; cur == ts {
				cur.lastHbPushMs = nowMs
			}
			b.mu.Unlock()
		}
	}
}

func parseCascadeIDList(raw string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if codexUUIDRe.MatchString(s) && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []string
		if json.Unmarshal([]byte(trimmed), &list) == nil {
			for _, item := range list {
				add(item)
				if len(out) >= codexMaxWatchedCids {
					break
				}
			}
			return out
		}
	}
	for _, part := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		add(part)
		if len(out) >= codexMaxWatchedCids {
			break
		}
	}
	return out
}

func (b *Bridge) refreshPane(paneKey string, force bool) {
	b.mu.Lock()
	p := b.panes[paneKey]
	if p == nil {
		b.mu.Unlock()
		return
	}
	push := b.targets[p.targetID]
	if push == nil {
		b.mu.Unlock()
		return
	}
	q := MetricsQuery{
		ConversationID:       p.conversationID,
		PaneSlotID:           p.paneSlotID,
		SessionRevision:      p.sessionRevision,
		MetricsSequence:      b.seq.Add(1),
		ModelID:              p.modelID,
		IsStreaming:          p.foregroundStreaming,
		ActivityPhase:        p.activityPhase,
		IsSubagentRunning:    p.isSubagentRunning,
		RunningSubagentCount: p.runningSubagentCount,
		ChildConversationIDs: append([]string(nil), p.childConversationIDs...),
	}
	b.mu.Unlock()

	m := b.engine.GetConversationMetrics(b.ctx, q)
	fp := fingerprintMetrics(&m)

	b.mu.Lock()
	cur := b.panes[paneKey]
	if cur == nil || cur != p || cur.conversationID != m.ConversationID ||
		cur.sessionRevision > m.SessionRevision || cur.metricsSequence > q.MetricsSequence {
		b.mu.Unlock()
		return
	}
	if !force && cur.lastFingerprint == fp {
		if q.MetricsSequence > cur.metricsSequence {
			cur.metricsSequence = q.MetricsSequence
		}
		b.mu.Unlock()
		return
	}
	push = b.targets[cur.targetID]
	b.mu.Unlock()

	if push == nil {
		return
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	expr := `((m) => Boolean(globalThis.__gravityboxUpdateHudMetrics ? globalThis.__gravityboxUpdateHudMetrics(m) : globalThis.__GRAVITYBOX_HUD_V1__?.dispatchMetrics?.(m)))(` + string(raw) + `)`
	ok, err := push(b.ctx, expr)
	if err != nil || !ok {
		return
	}

	b.mu.Lock()
	if b.panes[paneKey] == cur {
		cur.metricsSequence = q.MetricsSequence
		cur.lastFingerprint = fp
		cur.isStreaming = cur.foregroundStreaming || m.IsStreaming
		if m.ActivityPhase != "" {
			cur.activityPhase = m.ActivityPhase
		}
		if cur.foregroundStreaming || m.IsStreaming || m.IsSubagentRunning {
			cur.lastActivityAtMs = time.Now().UnixMilli()
		}
	}
	b.mu.Unlock()
}

func fingerprintMetrics(m *ConversationHudMetrics) string {
	copyM := *m
	copyM.UpdatedAt = 0
	copyM.MetricsSequence = 0
	if copyM.IsStreaming {
		copyM.Session.CurrentTurnDurationSeconds = 0
		copyM.Session.CurrentTurnDurationFormatted = ""
		copyM.Session.CurrentTurnDurationFormattedEn = ""
		copyM.Session.CurrentTurnDurationCompactZh = ""
		copyM.Session.CurrentTurnDurationCompactEn = ""
		copyM.Session.CurrentTurnModelDurationSeconds = 0
		copyM.Session.CurrentTurnToolDurationSeconds = 0
		copyM.Session.InFlightModelDurationSeconds = 0
		copyM.Session.InFlightToolDurationSeconds = 0
	} else {
		copyM.Session.CurrentTurnDurationSeconds = float64(int64(copyM.Session.CurrentTurnDurationSeconds + 0.5))
		copyM.Session.CurrentTurnModelDurationSeconds = float64(int64(copyM.Session.CurrentTurnModelDurationSeconds + 0.5))
		copyM.Session.CurrentTurnToolDurationSeconds = float64(int64(copyM.Session.CurrentTurnToolDurationSeconds + 0.5))
	}
	raw, _ := json.Marshal(copyM)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
