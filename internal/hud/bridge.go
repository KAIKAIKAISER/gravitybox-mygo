package hud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// BridgeBindingName is the CDP Runtime.addBinding name exposed to GravityBox isolated world.
	BridgeBindingName     = "__gravityboxHudBridge"
	activityGraceWindowMs = 15_000
)

// PushFunc evaluates an expression inside a CDP target's isolated world.
type PushFunc = func(ctx context.Context, expr string) (bool, error)

// ClientEvent is the event JSON sent from runtime/hud-instance.js via window.__gravityboxHudBridge.
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

// Bridge manages bidirectional HUD events & real-time metrics streaming across attached Antigravity panes.
type Bridge struct {
	engine  *MetricsEngine
	mu      sync.Mutex
	targets map[string]PushFunc
	panes   map[string]*activePane
	seq     atomic.Int64

	ctx    context.Context
	cancel context.CancelFunc
}

// NewBridge creates and starts a CdpHudBridge background polling loop.
func NewBridge(engine *MetricsEngine) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bridge{
		engine:  engine,
		targets: make(map[string]PushFunc),
		panes:   make(map[string]*activePane),
		ctx:     ctx,
		cancel:  cancel,
	}
	b.seq.Store(time.Now().UnixMilli())
	go b.pollLoop()
	return b
}

// Stop halts background polling.
func (b *Bridge) Stop() {
	b.cancel()
}

// AttachTarget registers a CDP page evaluation callback for pushing HUD updates.
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

// DetachTarget removes a CDP target and its tracked panes.
func (b *Bridge) DetachTarget(targetID string) {
	b.mu.Lock()
	delete(b.targets, targetID)
	prefix := targetID + "::"
	for k := range b.panes {
		if strings.HasPrefix(k, prefix) {
			delete(b.panes, k)
		}
	}
	b.mu.Unlock()
}

// RefreshAll clears pane fingerprints and pushes fresh HUD metrics immediately.
func (b *Bridge) RefreshAll() {
	b.mu.Lock()
	keys := make([]string, 0, len(b.panes))
	for k, p := range b.panes {
		p.lastFingerprint = ""
		keys = append(keys, k)
	}
	b.mu.Unlock()
	for _, k := range keys {
		b.refreshPane(k, true)
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
	var lastPollMs int64

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			nowMs := time.Now().UnixMilli()
			b.mu.Lock()
			if len(b.panes) == 0 {
				b.mu.Unlock()
				continue
			}
			anyActive := false
			keys := make([]string, 0, len(b.panes))
			for k, p := range b.panes {
				keys = append(keys, k)
				if p.isStreaming || p.isSubagentRunning || (nowMs-p.lastActivityAtMs < activityGraceWindowMs) {
					anyActive = true
				}
			}
			b.mu.Unlock()

			interval := int64(1000)
			if anyActive {
				interval = 300
			}
			if nowMs-lastPollMs < interval-20 {
				continue
			}
			lastPollMs = nowMs
			for _, k := range keys {
				b.refreshPane(k, false)
			}
		}
	}
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
