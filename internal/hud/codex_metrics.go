package hud

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

var codexUUIDRe = regexp.MustCompile(`(?i)^[0-9a-f-]{36}$`)

// CodexTelemetryOptions controls layout, cache mode, context limit & log probing for Codex telemetry.
type CodexTelemetryOptions struct {
	ManualContextLimit   int64
	FallbackContextLimit int64
	TelemetryLayout      string
	TelemetryCacheMode   string
	LsLogFile            string
	LsSig                string
}

// CodexStatusPayload matches payload.status expected by runtime 40-status-machine.js & 50-telemetry-ui.js.
type CodexStatusPayload struct {
	PendingKind      string `json:"pendingKind"`
	PendingRetrying  bool   `json:"pendingRetrying"`
	RetryReason      string `json:"retryReason"`
	LastErrorMsg     string `json:"lastErrorMsg"`
	TotalSteps       int    `json:"totalSteps"`
	TotalGenerations int    `json:"totalGenerations"`
	LastModelLabel   string `json:"lastModelLabel"`
}

// CodexStatsPayload matches payload.stats in core/daemon/sqlite-metrics.mjs.
type CodexStatsPayload struct {
	GenIdx             int     `json:"genIdx"`
	Tps                int64   `json:"tps"`
	StreamTps          float64 `json:"streamTps"`
	E2eTps             float64 `json:"e2eTps"`
	TTFT               float64 `json:"ttft"`
	StreamDur          float64 `json:"streamDur"`
	TotDur             float64 `json:"totDur"`
	Ctx                int64   `json:"ctx"`
	InTok              int64   `json:"inTok"`
	CacheWrite         int64   `json:"cacheWrite"`
	CacheRead          int64   `json:"cacheRead"`
	OutTok             int64   `json:"outTok"`
	ContextLimit       int64   `json:"contextLimit"`
	LimitStr           string  `json:"limitStr"`
	RemainCtx          int64   `json:"remainCtx"`
	CtxPct             int     `json:"ctxPct"`
	CtxClass           string  `json:"ctxClass"`
	CompactedPending   bool    `json:"compactedPending"`
	CompactSummaryKb   float64 `json:"compactSummaryKb"`
	CompactingNow      bool    `json:"compactingNow"`
	GeneratingPending  bool    `json:"generatingPending"`
	LastModel          string  `json:"lastModel"`
	LastCacheRead      int64   `json:"lastCacheRead"`
	PendingKind        string  `json:"pendingKind"`
	PendingStepIdx     int     `json:"pendingStepIdx"`
	PendingStartedAtMs int64   `json:"pendingStartedAtMs"`
	PendingPayloadLen  int     `json:"pendingPayloadLen"`
	PendingRetrying    bool    `json:"pendingRetrying"`
	PendingRetryCount  int     `json:"pendingRetryCount"`
	PendingRetryReason string  `json:"pendingRetryReason"`
	LastErrorMsg       string  `json:"lastErrorMsg"`
	StatusLevel        string  `json:"statusLevel"`
	StatusDotHTML      string  `json:"statusDotHtml"`
	StepCacheRate      string  `json:"stepCacheRate"`
	SessionCacheRate   string  `json:"sessionCacheRate"`
}

// CodexMetricsPayload is pushed verbatim to window.__ag_render_metrics(payload).
type CodexMetricsPayload struct {
	Cid            string               `json:"cid"`
	CascadeID      string               `json:"cascadeId"`
	HasData        bool                 `json:"hasData"`
	Sig            string               `json:"sig,omitempty"`
	InlineHTML     string               `json:"inlineHtml,omitempty"`
	TooltipHTML    string               `json:"tooltipHtml,omitempty"`
	Turns          []CodexTurnSummary   `json:"turns,omitempty"`
	Status         *CodexStatusPayload  `json:"status,omitempty"`
	Stats          *CodexStatsPayload   `json:"stats,omitempty"`
	TransientError bool                 `json:"transientError,omitempty"`
	CachedPayload  *CodexMetricsPayload `json:"-"`
}

type codexIncrementalState struct {
	rowCount          int
	maxIdx            int
	userStepsCount    int
	lastUserStepIdx   int
	lastUserStepSig   string
	totalStepsCount   int
	maxStepIdxAll     int
	lastCheckpointIdx int
	lastGenSig        string
	items             []CodexGenItem
	validItems        []CodexGenItem
	totalIn           int64
	totalCacheWrite   int64
	totalCache        int64
	totalOut          int64
	totalThink        int64
	totalResp         int64
}

type codexCachedEntry struct {
	sig     string
	payload *CodexMetricsPayload
}

type activeStepSummary struct {
	idx      int
	stepType int
	status   int
	metadata []byte
	plen     int
}

type stepBlobSummary struct {
	idx          int
	stepType     int
	status       int
	errorDetails []byte
	stepPayload  []byte
	metadata     []byte
}

// GetCodexDbSignature returns "<mtimeMs>:<size>|<walMtimeMs>:<walSize>" for a conversation DB.
func (e *MetricsEngine) GetCodexDbSignature(cascadeID string) string {
	if !codexUUIDRe.MatchString(strings.TrimSpace(cascadeID)) {
		return ""
	}
	dbPath := e.resolveDbPath(cascadeID)
	st, err := os.Stat(dbPath)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	walSig := "0:0"
	if wst, err := os.Stat(dbPath + "-wal"); err == nil {
		walSig = fmt.Sprintf("%d:%d", wst.ModTime().UnixMilli(), wst.Size())
	}
	return fmt.Sprintf("%d:%d|%s", st.ModTime().UnixMilli(), st.Size(), walSig)
}

// ParseConversationCodexMetrics computes the antigravity-codex-theme telemetry payload with incremental gen_metadata caching.
func (e *MetricsEngine) ParseConversationCodexMetrics(ctx context.Context, cascadeID string, opts CodexTelemetryOptions) *CodexMetricsPayload {
	cascadeID = strings.TrimSpace(cascadeID)
	if !codexUUIDRe.MatchString(cascadeID) {
		return nil
	}
	rawDbSig := e.GetCodexDbSignature(cascadeID)
	if rawDbSig == "" {
		return &CodexMetricsPayload{Cid: cascadeID, CascadeID: cascadeID, HasData: false}
	}

	manualLim := opts.ManualContextLimit
	fallbackLim := opts.FallbackContextLimit
	if fallbackLim <= 0 {
		fallbackLim = 256000
	}
	layout := "compact"
	if opts.TelemetryLayout == "detailed" {
		layout = "detailed"
	}
	cacheMode := "session"
	if opts.TelemetryCacheMode == "step" {
		cacheMode = "step"
	}

	lsPart := ""
	if opts.LsSig != "" {
		lsPart = "|ls:" + opts.LsSig
	}
	sig := fmt.Sprintf("%s|lim:%d:%d|ly:%s:%s%s", rawDbSig, manualLim, fallbackLim, layout, cacheMode, lsPart)

	dbPath := e.resolveDbPath(cascadeID)
	e.mu.Lock()
	if e.codexCache != nil {
		if cached, ok := e.codexCache[dbPath]; ok && cached.sig == sig {
			e.mu.Unlock()
			return cached.payload
		}
	}
	var cachedPayload *CodexMetricsPayload
	if e.codexCache != nil {
		if cached, ok := e.codexCache[dbPath]; ok {
			cachedPayload = cached.payload
		}
	}
	e.mu.Unlock()

	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	db, err := sqlite.Open(qctx, dbPath, sqlite.OpenOptions{ReadOnly: true})
	if err != nil {
		return &CodexMetricsPayload{
			Cid:            cascadeID,
			CascadeID:      cascadeID,
			TransientError: true,
			CachedPayload:  cachedPayload,
		}
	}
	defer db.Close()
	_, _ = db.Query(qctx, "PRAGMA query_only = ON; PRAGMA busy_timeout = 200")

	hasGenTable := false
	if tbls, err := db.Query(qctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='gen_metadata'"); err == nil && len(tbls.Values) > 0 {
		hasGenTable = true
	}

	hasPayloadCol, hasErrCol := false, false
	if cols, err := db.Query(qctx, "PRAGMA table_info(steps)"); err == nil {
		for _, r := range cols.Values {
			if len(r) < 2 {
				continue
			}
			colName, _ := r[1].(string)
			if colName == "step_payload" {
				hasPayloadCol = true
			} else if colName == "error_details" {
				hasErrCol = true
			}
		}
	}

	var rowCount, maxIdx int
	if hasGenTable {
		if mres, err := db.Query(qctx, "SELECT COUNT(*) AS cnt, COALESCE(MAX(idx), -1) AS maxIdx FROM gen_metadata"); err == nil && len(mres.Values) > 0 && len(mres.Values[0]) >= 2 {
			rowCount = toInt(mres.Values[0][0])
			maxIdx = toInt(mres.Values[0][1])
		} else if err != nil {
			return &CodexMetricsPayload{Cid: cascadeID, CascadeID: cascadeID, TransientError: true, CachedPayload: cachedPayload}
		}
	}

	// Fallback synthesis when gen_metadata table is absent/empty (e.g., older or test DBs with steps only).
	var synthGenItems []CodexGenItem
	if rowCount == 0 || maxIdx < 0 {
		sRows, err := db.Query(qctx, "SELECT idx, step_type, status, metadata FROM steps WHERE step_type = 15 ORDER BY idx ASC")
		if err == nil && len(sRows.Values) > 0 {
			for _, sr := range sRows.Values {
				if len(sr) < 4 {
					continue
				}
				sIdx := toInt(sr[0])
				stType := toInt(sr[1])
				stStat := toInt(sr[2])
				ps := ParseStepMetadataBlob(sIdx, stType, stStat, toBytes(sr[3]), nil, nil, nil)
				if !ps.HasUsage {
					continue
				}
				ttft := 0.0
				stream := 0.0
				if ps.StartTimeSeconds != nil && ps.EndTimeSeconds != nil && *ps.EndTimeSeconds > *ps.StartTimeSeconds {
					total := *ps.EndTimeSeconds - *ps.StartTimeSeconds
					if ps.FirstTokenSeconds != nil && *ps.FirstTokenSeconds >= *ps.StartTimeSeconds && *ps.FirstTokenSeconds <= *ps.EndTimeSeconds {
						ttft = *ps.FirstTokenSeconds - *ps.StartTimeSeconds
						stream = *ps.EndTimeSeconds - *ps.FirstTokenSeconds
					} else {
						stream = total
					}
				}
				synthGenItems = append(synthGenItems, CodexGenItem{
					Idx:        sIdx,
					InTok:      ps.RawInputTokens,
					OutTok:     ps.OutputTokens,
					CacheWrite: ps.CacheWriteTokens,
					CacheRead:  ps.CachedReadTokens,
					Ctx:        ps.RawInputTokens + ps.CacheWriteTokens + ps.CachedReadTokens,
					MaxCtx:     ps.ModelMaxTokens,
					MaxStepIdx: sIdx,
					ModelName:  ps.ModelName,
					ThinkTok:   ps.ReasoningTokens,
					RespTok:    ps.CandidateTokens,
					TTFT:       ttft,
					Stream:     stream,
					TotDur:     ttft + stream,
				})
			}
		}
		if len(synthGenItems) == 0 {
			e.deleteCodexInc(dbPath)
			empty := &CodexMetricsPayload{Cid: cascadeID, CascadeID: cascadeID, HasData: false, Sig: sig}
			e.setCodexCache(dbPath, sig, empty)
			return empty
		}
	}

	// Query step_type = 14 (user turn steps).
	userRows, err := db.Query(qctx, "SELECT idx FROM steps WHERE step_type = 14 ORDER BY idx ASC")
	if err != nil {
		return &CodexMetricsPayload{Cid: cascadeID, CascadeID: cascadeID, TransientError: true, CachedPayload: cachedPayload}
	}
	userStepIdxs := make([]int, 0, len(userRows.Values))
	for _, r := range userRows.Values {
		if len(r) > 0 {
			userStepIdxs = append(userStepIdxs, toInt(r[0]))
		}
	}
	userStepsCount := len(userStepIdxs)
	lastUserStepIdx := -1
	if userStepsCount > 0 {
		lastUserStepIdx = userStepIdxs[userStepsCount-1]
	}

	var lastUserStepSig string
	if lastUserStepIdx >= 0 {
		pLenExpr := "0 AS plen"
		if hasPayloadCol {
			pLenExpr = "COALESCE(length(step_payload), 0) AS plen"
		}
		if uMetaRes, err := db.Query(qctx, "SELECT metadata, "+pLenExpr+" FROM steps WHERE idx = ?", lastUserStepIdx); err == nil && len(uMetaRes.Values) > 0 && len(uMetaRes.Values[0]) >= 2 {
			lastUserStepSig = ExtractUserStepSig(lastUserStepIdx, toBytes(uMetaRes.Values[0][0]), toInt(uMetaRes.Values[0][1]))
		}
	}

	totalStepsCount, maxStepIdxAll := 0, -1
	if stStatRes, err := db.Query(qctx, "SELECT COUNT(*) AS cnt, COALESCE(MAX(idx), -1) AS maxIdx FROM steps"); err == nil && len(stStatRes.Values) > 0 && len(stStatRes.Values[0]) >= 2 {
		totalStepsCount = toInt(stStatRes.Values[0][0])
		maxStepIdxAll = toInt(stStatRes.Values[0][1])
	}

	var lastCheckpoint *CheckpointStepInfo
	cpCols := "idx, status, metadata, NULL AS step_payload"
	if hasPayloadCol {
		cpCols = "idx, status, metadata, step_payload"
	}
	if cpRes, err := db.Query(qctx, "SELECT "+cpCols+" FROM steps WHERE step_type = 23 AND status = 3 ORDER BY idx DESC LIMIT 1"); err == nil && len(cpRes.Values) > 0 && len(cpRes.Values[0]) >= 4 {
		r := cpRes.Values[0]
		lastCheckpoint = ParseCheckpointStepInfo(toInt(r[0]), toInt(r[1]), toBytes(r[2]), toBytes(r[3]))
	}
	lastCheckpointIdx := -1
	if lastCheckpoint != nil {
		lastCheckpointIdx = lastCheckpoint.Idx
	}

	actCols := "idx, step_type, status, metadata, 0 AS plen"
	if hasPayloadCol {
		actCols = "idx, step_type, status, metadata, COALESCE(length(step_payload), 0) AS plen"
	}
	var activeStepRows []activeStepSummary
	if actRes, err := db.Query(qctx, "SELECT "+actCols+" FROM steps WHERE status IN (1, 2, 8, 9, 11) ORDER BY idx DESC LIMIT 10"); err == nil {
		for _, r := range actRes.Values {
			if len(r) >= 5 {
				activeStepRows = append(activeStepRows, activeStepSummary{
					idx:      toInt(r[0]),
					stepType: toInt(r[1]),
					status:   toInt(r[2]),
					metadata: toBytes(r[3]),
					plen:     toInt(r[4]),
				})
			}
		}
	}

	errColExpr := "NULL AS error_details"
	if hasErrCol {
		errColExpr = "error_details"
	}
	payColExpr := "NULL AS step_payload"
	if hasPayloadCol {
		payColExpr = "step_payload"
	}
	blobCols := "idx, step_type, status, " + errColExpr + ", " + payColExpr + ", metadata"

	var lastModelStepRow *stepBlobSummary
	if lmRes, err := db.Query(qctx, "SELECT "+blobCols+" FROM steps WHERE step_type IN (15, 17, 23) ORDER BY idx DESC LIMIT 1"); err == nil && len(lmRes.Values) > 0 && len(lmRes.Values[0]) >= 6 {
		r := lmRes.Values[0]
		lastModelStepRow = &stepBlobSummary{
			idx:          toInt(r[0]),
			stepType:     toInt(r[1]),
			status:       toInt(r[2]),
			errorDetails: toBytes(r[3]),
			stepPayload:  toBytes(r[4]),
			metadata:     toBytes(r[5]),
		}
	}

	var latestNonUserStep *stepBlobSummary
	if lnuRes, err := db.Query(qctx, "SELECT "+blobCols+" FROM steps WHERE step_type != 14 ORDER BY idx DESC LIMIT 1"); err == nil && len(lnuRes.Values) > 0 && len(lnuRes.Values[0]) >= 6 {
		r := lnuRes.Values[0]
		latestNonUserStep = &stepBlobSummary{
			idx:          toInt(r[0]),
			stepType:     toInt(r[1]),
			status:       toInt(r[2]),
			errorDetails: toBytes(r[3]),
			stepPayload:  toBytes(r[4]),
			metadata:     toBytes(r[5]),
		}
	}

	rawTurnSteps := 0
	if turnRes, err := db.Query(qctx, "SELECT COUNT(*) AS cnt FROM steps WHERE step_type = 15 AND idx > ?", lastUserStepIdx); err == nil && len(turnRes.Values) > 0 && len(turnRes.Values[0]) > 0 {
		rawTurnSteps = toInt(turnRes.Values[0][0])
	}

	var inc *codexIncrementalState
	if len(synthGenItems) > 0 {
		inc = &codexIncrementalState{
			rowCount:          len(synthGenItems),
			maxIdx:            synthGenItems[len(synthGenItems)-1].Idx,
			userStepsCount:    userStepsCount,
			lastUserStepIdx:   lastUserStepIdx,
			lastUserStepSig:   lastUserStepSig,
			totalStepsCount:   totalStepsCount,
			maxStepIdxAll:     maxStepIdxAll,
			lastCheckpointIdx: lastCheckpointIdx,
		}
		for _, p := range synthGenItems {
			inc.items = append(inc.items, p)
			inc.totalIn += p.InTok
			inc.totalCacheWrite += p.CacheWrite
			inc.totalCache += p.CacheRead
			inc.totalOut += p.OutTok
			inc.totalThink += p.ThinkTok
			inc.totalResp += p.RespTok
			if p.Ctx > 0 || p.OutTok > 0 {
				inc.validItems = append(inc.validItems, p)
			}
		}
	} else {
		var lastGenSig string
		if lgRes, err := db.Query(qctx, "SELECT size, COALESCE(length(data), 0) AS dlen FROM gen_metadata WHERE idx = ?", maxIdx); err == nil && len(lgRes.Values) > 0 && len(lgRes.Values[0]) >= 2 {
			lastGenSig = fmt.Sprintf("%d:%d:%d", maxIdx, toInt(lgRes.Values[0][0]), toInt(lgRes.Values[0][1]))
		}

		inc = e.getCodexInc(dbPath)
		if inc != nil && (rowCount < inc.rowCount ||
			maxIdx < inc.maxIdx ||
			totalStepsCount < inc.totalStepsCount ||
			maxStepIdxAll < inc.maxStepIdxAll ||
			userStepsCount < inc.userStepsCount ||
			lastUserStepIdx < inc.lastUserStepIdx ||
			lastCheckpointIdx < inc.lastCheckpointIdx ||
			(lastUserStepIdx == inc.lastUserStepIdx && lastUserStepSig != inc.lastUserStepSig) ||
			(rowCount == inc.rowCount && maxIdx == inc.maxIdx && lastGenSig != inc.lastGenSig) ||
			(lastUserStepIdx != inc.lastUserStepIdx && userStepsCount <= inc.userStepsCount)) {
			inc = nil
		}

		if inc != nil {
			startIdx := inc.maxIdx
			deltaRes, derr := db.Query(qctx, "SELECT idx, data FROM gen_metadata WHERE idx >= ? ORDER BY idx ASC", startIdx)
			if derr != nil {
				return &CodexMetricsPayload{Cid: cascadeID, CascadeID: cascadeID, TransientError: true, CachedPayload: cachedPayload}
			}
			for len(inc.items) > 0 && inc.items[len(inc.items)-1].Idx >= startIdx {
				popped := inc.items[len(inc.items)-1]
				inc.items = inc.items[:len(inc.items)-1]
				inc.totalIn -= popped.InTok
				inc.totalCacheWrite -= popped.CacheWrite
				inc.totalCache -= popped.CacheRead
				inc.totalOut -= popped.OutTok
				inc.totalThink -= popped.ThinkTok
				inc.totalResp -= popped.RespTok
			}
			for len(inc.validItems) > 0 && inc.validItems[len(inc.validItems)-1].Idx >= startIdx {
				inc.validItems = inc.validItems[:len(inc.validItems)-1]
			}
			if len(inc.items)+len(deltaRes.Values) == rowCount {
				for _, r := range deltaRes.Values {
					if len(r) < 2 {
						continue
					}
					p := ParseCodexGenRow(toInt(r[0]), toBytes(r[1]))
					inc.items = append(inc.items, p)
					inc.totalIn += p.InTok
					inc.totalCacheWrite += p.CacheWrite
					inc.totalCache += p.CacheRead
					inc.totalOut += p.OutTok
					inc.totalThink += p.ThinkTok
					inc.totalResp += p.RespTok
					if p.Ctx > 0 || p.OutTok > 0 {
						inc.validItems = append(inc.validItems, p)
					}
				}
				inc.rowCount = rowCount
				inc.maxIdx = maxIdx
				inc.userStepsCount = userStepsCount
				inc.lastUserStepIdx = lastUserStepIdx
				inc.lastUserStepSig = lastUserStepSig
				inc.totalStepsCount = totalStepsCount
				inc.maxStepIdxAll = maxStepIdxAll
				inc.lastCheckpointIdx = lastCheckpointIdx
				inc.lastGenSig = lastGenSig
			} else {
				inc = nil
			}
		}

		if inc == nil {
			allRes, aerr := db.Query(qctx, "SELECT idx, data FROM gen_metadata ORDER BY idx ASC")
			if aerr != nil {
				return &CodexMetricsPayload{Cid: cascadeID, CascadeID: cascadeID, TransientError: true, CachedPayload: cachedPayload}
			}
			next := &codexIncrementalState{
				rowCount:          len(allRes.Values),
				maxIdx:            -1,
				userStepsCount:    userStepsCount,
				lastUserStepIdx:   lastUserStepIdx,
				lastUserStepSig:   lastUserStepSig,
				totalStepsCount:   totalStepsCount,
				maxStepIdxAll:     maxStepIdxAll,
				lastCheckpointIdx: lastCheckpointIdx,
				lastGenSig:        lastGenSig,
			}
			for _, r := range allRes.Values {
				if len(r) < 2 {
					continue
				}
				p := ParseCodexGenRow(toInt(r[0]), toBytes(r[1]))
				next.maxIdx = p.Idx
				next.items = append(next.items, p)
				next.totalIn += p.InTok
				next.totalCacheWrite += p.CacheWrite
				next.totalCache += p.CacheRead
				next.totalOut += p.OutTok
				next.totalThink += p.ThinkTok
				next.totalResp += p.RespTok
				if p.Ctx > 0 || p.OutTok > 0 {
					next.validItems = append(next.validItems, p)
				}
			}
			inc = next
		}
		e.setCodexInc(dbPath, inc)
	}

	valid := inc.validItems
	if len(valid) == 0 {
		empty := &CodexMetricsPayload{Cid: cascadeID, CascadeID: cascadeID, HasData: false, Sig: sig}
		e.setCodexCache(dbPath, sig, empty)
		return empty
	}

	hasAnyStepIdx := false
	for _, it := range valid {
		if it.MaxStepIdx >= 0 {
			hasAnyStepIdx = true
			break
		}
	}

	var turnSlice []CodexGenItem
	if lastUserStepIdx >= 0 && hasAnyStepIdx {
		for i := len(valid) - 1; i >= 0; i-- {
			si := valid[i].MaxStepIdx
			if si > lastUserStepIdx {
				turnSlice = append(turnSlice, valid[i])
			} else if si >= 0 {
				break
			}
		}
		for i, j := 0, len(turnSlice)-1; i < j; i, j = i+1, j-1 {
			turnSlice[i], turnSlice[j] = turnSlice[j], turnSlice[i]
		}
	} else if rawTurnSteps > 0 {
		start := len(valid) - rawTurnSteps
		if start < 0 {
			start = 0
		}
		turnSlice = valid[start:]
	}

	turnSteps := len(turnSlice)
	var turnOut, turnThink, turnResp int64
	var turnDur float64
	for _, ts := range turnSlice {
		turnOut += ts.OutTok
		turnThink += ts.ThinkTok
		turnResp += ts.RespTok
		turnDur += ts.TotDur
	}

	last := valid[len(valid)-1]
	nativeContextLimit := last.MaxCtx
	if nativeContextLimit <= 0 {
		for i := len(valid) - 1; i >= 0; i-- {
			if valid[i].MaxCtx > 0 {
				nativeContextLimit = valid[i].MaxCtx
				break
			}
		}
	}
	if nativeContextLimit <= 0 {
		for i := len(inc.items) - 1; i >= 0; i-- {
			if inc.items[i].MaxCtx > 0 {
				nativeContextLimit = inc.items[i].MaxCtx
				break
			}
		}
	}
	contextLimitTokens := fallbackLim
	if manualLim > 0 {
		contextLimitTokens = manualLim
	} else if nativeContextLimit > 0 {
		contextLimitTokens = nativeContextLimit
	}
	if contextLimitTokens <= 0 {
		contextLimitTokens = 256000
	}

	const minStreamDurSec = 0.12
	const maxSanityStreamTps = 3000.0
	streamTps := 0.0
	if last.Stream >= minStreamDurSec {
		streamTps = float64(last.OutTok) / last.Stream
	}
	e2eTps := 0.0
	if last.TotDur > 0 {
		e2eTps = float64(last.OutTok) / last.TotDur
	}
	displayTps := e2eTps
	if streamTps > 0 && streamTps <= maxSanityStreamTps {
		displayTps = streamTps
	}

	ctxPct := int(math.Round((float64(last.Ctx) / float64(contextLimitTokens)) * 100))
	ctxClass := "ag-ctx-ok"
	if ctxPct >= 90 {
		ctxClass = "ag-ctx-crit"
	} else if ctxPct >= 75 {
		ctxClass = "ag-ctx-warn"
	}

	lastCacheRate := "0.0"
	if last.Ctx > 0 {
		lastCacheRate = fmt.Sprintf("%.1f", (float64(last.CacheRead)/float64(last.Ctx))*100)
	}
	totalPrompt := inc.totalIn + inc.totalCacheWrite + inc.totalCache
	sessionCacheRate := "0.0"
	if totalPrompt > 0 {
		sessionCacheRate = fmt.Sprintf("%.1f", (float64(inc.totalCache)/float64(totalPrompt))*100)
	}
	remainCtx := contextLimitTokens - last.Ctx
	if remainCtx < 0 {
		remainCtx = 0
	}
	limitKStr := CodexFormatContextLimitLabel(contextLimitTokens)

	compactedPending := false
	if lastCheckpoint != nil {
		if last.MaxStepIdx >= 0 {
			compactedPending = lastCheckpoint.Idx > last.MaxStepIdx
		} else {
			compactedPending = lastCheckpoint.CpMetaIdx > last.Idx
		}
	}
	compactSummaryKb := 0.0
	if compactedPending && lastCheckpoint != nil && lastCheckpoint.SummaryBytes > 0 {
		compactSummaryKb = math.Round((float64(lastCheckpoint.SummaryBytes)/1024.0)*10) / 10
	}

	var activeCompactRow *activeStepSummary
	var nonUserActive []activeStepSummary
	for i := range activeStepRows {
		r := activeStepRows[i]
		if r.stepType == 23 && activeCompactRow == nil {
			cp := r
			activeCompactRow = &cp
		}
		if r.stepType != 14 && (last.MaxStepIdx < 0 || r.idx > last.MaxStepIdx) {
			nonUserActive = append(nonUserActive, r)
		}
	}
	compactingNow := activeCompactRow != nil
	generatingPending := compactingNow || len(nonUserActive) > 0
	var latestActiveRow *activeStepSummary
	if activeCompactRow != nil {
		latestActiveRow = activeCompactRow
	} else if len(nonUserActive) > 0 {
		latestActiveRow = &nonUserActive[0]
	}

	pendingKind := "idle"
	pendingStepIdx := -1
	var pendingStartedAtMs int64
	pendingPayloadLen := 0
	if latestActiveRow != nil {
		switch latestActiveRow.stepType {
		case 23:
			pendingKind = "compact"
		case 15:
			pendingKind = "llm"
		default:
			pendingKind = "tool"
		}
		pendingStepIdx = latestActiveRow.idx
		pendingStartedAtMs = ExtractStepStartMs(latestActiveRow.metadata)
		pendingPayloadLen = latestActiveRow.plen
	}

	var latestAnyGen *CodexGenItem
	if len(inc.items) > 0 {
		it := inc.items[len(inc.items)-1]
		latestAnyGen = &it
	}
	tailRetryErrCount := 0
	tailRetryErrMsg := ""
	for i := len(inc.items) - 1; i >= 0; i-- {
		it := inc.items[i]
		if it.ErrCode == 1 && it.ErrMsg != "" && (it.MaxStepIdx < 0 || it.MaxStepIdx >= lastUserStepIdx) {
			tailRetryErrCount++
			if tailRetryErrMsg == "" {
				tailRetryErrMsg = it.ErrMsg
			}
		} else {
			break
		}
	}

	step17InTail := latestNonUserStep != nil &&
		latestNonUserStep.stepType == 17 &&
		(lastUserStepIdx < 0 || latestNonUserStep.idx >= lastUserStepIdx) &&
		(last.MaxStepIdx < 0 || latestNonUserStep.idx > last.MaxStepIdx)
	step17RawMsg := ""
	if step17InTail {
		step17RawMsg = ExtractStep17Message(latestNonUserStep.stepPayload, latestNonUserStep.errorDetails)
	}
	step17TailActive := step17InTail && !IsCancelText(step17RawMsg)
	var step17TsMs int64
	step17Msg := ""
	if step17TailActive {
		step17TsMs = ExtractStepStartMs(latestNonUserStep.metadata)
		step17Msg = step17RawMsg
	}

	lsRetrySinceMs := step17TsMs
	if generatingPending {
		lsRetrySinceMs = pendingStartedAtMs
	}
	var lsRetryInfo *LsRetryInfo
	if (generatingPending || step17TailActive) && opts.LsLogFile != "" {
		lsRetryInfo = ReadRecentLsRetryInfo(opts.LsLogFile, lsRetrySinceMs, time.Now())
	}

	nowMs := time.Now().UnixMilli()
	pendingRetrying := (generatingPending && (tailRetryErrCount > 0 || (lsRetryInfo != nil && lsRetryInfo.Attempt > 0))) ||
		(step17TailActive && lsRetryInfo != nil && lsRetryInfo.Attempt > 0 && nowMs-lsRetryInfo.TsMs <= 240_000)

	pendingRetryCount := tailRetryErrCount
	if lsRetryInfo != nil && lsRetryInfo.Attempt > pendingRetryCount {
		pendingRetryCount = lsRetryInfo.Attempt
	}
	pendingRetryReason := tailRetryErrMsg
	if lsRetryInfo != nil && lsRetryInfo.Reason != "" {
		pendingRetryReason = lsRetryInfo.Reason
	} else if pendingRetryReason == "" {
		pendingRetryReason = step17Msg
	}

	stepErrMsg := ""
	if lastModelStepRow != nil && lastModelStepRow.idx >= lastUserStepIdx {
		if lastModelStepRow.status == 7 {
			rawDetail := "步骤执行失败 (ERROR)"
			if len(lastModelStepRow.errorDetails) > 0 {
				if cleaned := sanitizeAsciiCjkSpaces(string(lastModelStepRow.errorDetails)); cleaned != "" {
					rawDetail = truncateRunes(cleaned, 180)
				}
			}
			if !IsCancelText(rawDetail) {
				stepErrMsg = rawDetail
			}
		} else if step17TailActive {
			if step17Msg != "" {
				stepErrMsg = step17Msg
			} else {
				stepErrMsg = "API 调用失败 (ERROR_MESSAGE)"
			}
		}
	}

	latestGenErrored := !generatingPending &&
		latestAnyGen != nil &&
		latestAnyGen.ErrCode == 1 &&
		latestAnyGen.ErrMsg != "" &&
		!IsCancelText(latestAnyGen.ErrMsg) &&
		(lastUserStepIdx < 0 || latestAnyGen.MaxStepIdx < 0 || latestAnyGen.MaxStepIdx >= lastUserStepIdx)

	effectiveExecuting := generatingPending || pendingRetrying
	rawLastErrorMsg := ""
	if !effectiveExecuting {
		rawLastErrorMsg = stepErrMsg
		if rawLastErrorMsg == "" && latestGenErrored {
			rawLastErrorMsg = latestAnyGen.ErrMsg
		}
	}
	lastErrorMsg := ""
	if rawLastErrorMsg != "" && !IsCancelText(rawLastErrorMsg) {
		lastErrorMsg = rawLastErrorMsg
	}

	statusLevel := "idle"
	if lastErrorMsg != "" {
		statusLevel = "error"
	} else if pendingRetrying {
		statusLevel = "retrying"
	} else if generatingPending {
		statusLevel = "running"
	}

	statusDotClass := "ag-ctx-pending-dot ag-dot-idle"
	statusTitle := "会话状态：就绪 · 悬停或点击查看统计"
	switch statusLevel {
	case "error":
		statusDotClass = "ag-ctx-pending-dot ag-dot-error"
		statusTitle = "会话状态：异常 · " + truncateRunes(lastErrorMsg, 120)
	case "retrying":
		statusDotClass = "ag-ctx-pending-dot ag-dot-slow"
		reasonSuffix := ""
		if pendingRetryReason != "" {
			reasonSuffix = "：" + truncateRunes(pendingRetryReason, 100)
		}
		statusTitle = fmt.Sprintf("会话状态：等待或重试（第 %d 次%s）", pendingRetryCount, reasonSuffix)
	case "running":
		statusDotClass = "ag-ctx-pending-dot ag-dot-running"
		if compactingNow {
			statusTitle = "会话状态：执行中 · 上下文压缩中…"
		} else {
			statusTitle = "会话状态：执行中 · 新一步执行中"
		}
	}
	statusDotHTML := fmt.Sprintf(`<span class="%s" aria-label="%s"></span>`, statusDotClass, CodexEscapeHTML(statusTitle))

	turns := BuildCodexTurnSummaries(valid, userStepIdxs, manualLim, fallbackLim)
	inlineHTML := BuildCodexInlineTelemetryHTML(inlineTelemetryParams{
		TelemetryLayout:    layout,
		TelemetryCacheMode: cacheMode,
		StatusDotHTML:      statusDotHTML,
		DisplayTps:         displayTps,
		TTFT:               last.TTFT,
		LastCtx:            last.Ctx,
		LimitKStr:          limitKStr,
		CtxClass:           ctxClass,
		CtxPct:             ctxPct,
		CompactedPending:   compactedPending,
		OutTok:             last.OutTok,
		LastCacheRate:      lastCacheRate,
		SessionCacheRate:   sessionCacheRate,
	})

	streamTpsValid := streamTps > 0 && streamTps <= maxSanityStreamTps
	tooltipHTML := BuildCodexTooltipHTML(tooltipParams{
		GenIdx:             last.Idx + 1,
		StreamTps:          streamTps,
		E2eTps:             e2eTps,
		StreamTpsValid:     streamTpsValid,
		Last:               last,
		LastCacheRate:      lastCacheRate,
		CtxPct:             ctxPct,
		ContextLimitTokens: contextLimitTokens,
		LimitKStr:          limitKStr,
		RemainCtx:          remainCtx,
		CompactedPending:   compactedPending,
		CompactSummaryKb:   compactSummaryKb,
		CompactingNow:      compactingNow,
		GeneratingPending:  generatingPending,
		PendingRetrying:    pendingRetrying,
		LastErrorMsg:       lastErrorMsg,
		TurnSteps:          turnSteps,
		TurnOut:            turnOut,
		TurnThink:          turnThink,
		TurnResp:           turnResp,
		TurnDur:            turnDur,
		UserStepsCount:     userStepsCount,
		ValidCount:         len(valid),
		TotalIn:            inc.totalIn,
		TotalCacheWrite:    inc.totalCacheWrite,
		TotalCache:         inc.totalCache,
		TotalPrompt:        totalPrompt,
		SessionCacheRate:   sessionCacheRate,
		TotalOut:           inc.totalOut,
		TotalThink:         inc.totalThink,
		TotalResp:          inc.totalResp,
	})

	payload := &CodexMetricsPayload{
		Cid:         cascadeID,
		CascadeID:   cascadeID,
		HasData:     true,
		Sig:         sig,
		InlineHTML:  inlineHTML,
		TooltipHTML: tooltipHTML,
		Turns:       turns,
		Status: &CodexStatusPayload{
			PendingKind:      pendingKind,
			PendingRetrying:  pendingRetrying,
			RetryReason:      pendingRetryReason,
			LastErrorMsg:     lastErrorMsg,
			TotalSteps:       totalStepsCount,
			TotalGenerations: len(valid),
			LastModelLabel:   last.ModelName,
		},
		Stats: &CodexStatsPayload{
			GenIdx:             last.Idx + 1,
			Tps:                int64(math.Round(displayTps)),
			StreamTps:          math.Round(streamTps*10) / 10,
			E2eTps:             math.Round(e2eTps*10) / 10,
			TTFT:               math.Round(last.TTFT*100) / 100,
			StreamDur:          math.Round(last.Stream*100) / 100,
			TotDur:             math.Round(last.TotDur*100) / 100,
			Ctx:                last.Ctx,
			InTok:              last.InTok,
			CacheWrite:         last.CacheWrite,
			CacheRead:          last.CacheRead,
			OutTok:             last.OutTok,
			ContextLimit:       contextLimitTokens,
			LimitStr:           limitKStr,
			RemainCtx:          remainCtx,
			CtxPct:             ctxPct,
			CtxClass:           ctxClass,
			CompactedPending:   compactedPending,
			CompactSummaryKb:   compactSummaryKb,
			CompactingNow:      compactingNow,
			GeneratingPending:  generatingPending,
			LastModel:          last.ModelName,
			LastCacheRead:      last.CacheRead,
			PendingKind:        pendingKind,
			PendingStepIdx:     pendingStepIdx,
			PendingStartedAtMs: pendingStartedAtMs,
			PendingPayloadLen:  pendingPayloadLen,
			PendingRetrying:    pendingRetrying,
			PendingRetryCount:  pendingRetryCount,
			PendingRetryReason: pendingRetryReason,
			LastErrorMsg:       lastErrorMsg,
			StatusLevel:        statusLevel,
			StatusDotHTML:      statusDotHTML,
			StepCacheRate:      lastCacheRate + "%",
			SessionCacheRate:   sessionCacheRate + "%",
		},
	}
	e.setCodexCache(dbPath, sig, payload)
	return payload
}

func (e *MetricsEngine) getCodexInc(dbPath string) *codexIncrementalState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.codexInc == nil {
		return nil
	}
	return e.codexInc[dbPath]
}

func (e *MetricsEngine) setCodexInc(dbPath string, st *codexIncrementalState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.codexInc == nil {
		e.codexInc = make(map[string]*codexIncrementalState)
	}
	if len(e.codexInc) >= 30 {
		for k := range e.codexInc {
			if k != dbPath {
				delete(e.codexInc, k)
				break
			}
		}
	}
	e.codexInc[dbPath] = st
}

func (e *MetricsEngine) deleteCodexInc(dbPath string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.codexInc != nil {
		delete(e.codexInc, dbPath)
	}
}

func (e *MetricsEngine) setCodexCache(dbPath, sig string, p *CodexMetricsPayload) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.codexCache == nil {
		e.codexCache = make(map[string]*codexCachedEntry)
	}
	if len(e.codexCache) >= 30 {
		for k := range e.codexCache {
			if k != dbPath {
				delete(e.codexCache, k)
				break
			}
		}
	}
	e.codexCache[dbPath] = &codexCachedEntry{sig: sig, payload: p}
}
