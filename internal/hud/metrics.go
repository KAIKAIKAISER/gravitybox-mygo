package hud

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

const turnStepHandoffGraceSec = 600.0

var (
	uuidSanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	nonToolTypes   = map[int]bool{
		0: true, 1: true, 2: true, 14: true, 15: true, 23: true,
		81: true, 98: true, 99: true, 101: true, 102: true, 104: true,
	}
	transparentHandoffTypes = map[int]bool{81: true, 98: true, 99: true, 101: true, 102: true, 104: true}
	activeStepStatuses      = map[int]bool{1: true, 2: true, 8: true, 9: true}
	runningSubagentStatuses = map[int]bool{1: true, 2: true, 8: true}
)

// ConversationHudMetrics mirrors src/hud/types.ts ConversationHudMetrics JSON schema.
type ConversationHudMetrics struct {
	ConversationID       string                 `json:"conversationId"`
	PaneSlotID           string                 `json:"paneSlotId,omitempty"`
	SessionRevision      int64                  `json:"sessionRevision"`
	MetricsSequence      int64                  `json:"metricsSequence,omitempty"`
	Availability         string                 `json:"availability,omitempty"`
	ObservedAt           *int64                 `json:"observedAt"`
	LastSuccessAt        *int64                 `json:"lastSuccessAt"`
	FailedChildCount     int                    `json:"failedChildCount,omitempty"`
	UpdatedAt            int64                  `json:"updatedAt"`
	IsStreaming          bool                   `json:"isStreaming"`
	ActivityPhase        string                 `json:"activityPhase,omitempty"`
	IsSubagentRunning    bool                   `json:"isSubagentRunning,omitempty"`
	RunningSubagentCount int                    `json:"runningSubagentCount,omitempty"`
	Session              HudSessionMetrics      `json:"session"`
	Tokens               HudTokenMetrics        `json:"tokens"`
	Context              HudContextMetrics      `json:"context"`
}

type HudSampleCounts struct {
	Usage int `json:"usage"`
	Timed int `json:"timed"`
	TTFT  int `json:"ttft"`
}

type HudSessionMetrics struct {
	Turns                           int              `json:"turns"`
	Steps                           int              `json:"steps"`
	ModelSteps                      int              `json:"modelSteps,omitempty"`
	Samples                         *HudSampleCounts `json:"samples,omitempty"`
	ToolSteps                       int              `json:"toolSteps,omitempty"`
	CompactionCount                 int              `json:"compactionCount,omitempty"`
	SubagentCount                   int              `json:"subagentCount,omitempty"`
	RunningSubagentCount            int              `json:"runningSubagentCount,omitempty"`
	SubagentSteps                   int              `json:"subagentSteps,omitempty"`
	HasSubagentInTurn               bool             `json:"hasSubagentInTurn,omitempty"`
	IsSubagentTrajectory            bool             `json:"isSubagentTrajectory,omitempty"`
	AwaitingFirstTurnStep           bool             `json:"awaitingFirstTurnStep,omitempty"`
	TurnAborted                     bool             `json:"turnAborted,omitempty"`
	TurnAbortedMissingEnd           bool             `json:"turnAbortedMissingEnd,omitempty"`
	ModelDurationSeconds            float64          `json:"modelDurationSeconds"`
	ModelDurationFormatted          string           `json:"modelDurationFormatted"`
	ModelDurationFormattedEn        string           `json:"modelDurationFormattedEn,omitempty"`
	ToolDurationSeconds             float64          `json:"toolDurationSeconds"`
	ToolDurationFormatted           string           `json:"toolDurationFormatted"`
	ToolDurationFormattedEn         string           `json:"toolDurationFormattedEn,omitempty"`
	AverageTtftSeconds              float64          `json:"averageTtftSeconds"`
	AverageTtftFormatted            string           `json:"averageTtftFormatted"`
	AverageTtftFormattedEn          string           `json:"averageTtftFormattedEn,omitempty"`
	OutputTps                       float64          `json:"outputTps"`
	OutputTpsFormatted              string           `json:"outputTpsFormatted"`
	OutputTpsPending                bool             `json:"outputTpsPending,omitempty"`
	LatestStepOutputTps             *float64         `json:"latestStepOutputTps,omitempty"`
	LatestStepOutputTpsFormatted    string           `json:"latestStepOutputTpsFormatted,omitempty"`
	LatestStepTtftSeconds           *float64         `json:"latestStepTtftSeconds,omitempty"`
	LatestStepTtftFormatted         string           `json:"latestStepTtftFormatted,omitempty"`
	LatestStepTtftFormattedEn       string           `json:"latestStepTtftFormattedEn,omitempty"`
	CurrentTurnOutputTps            float64          `json:"currentTurnOutputTps,omitempty"`
	CurrentTurnOutputTpsFormatted   string           `json:"currentTurnOutputTpsFormatted,omitempty"`
	CurrentTurnOutputTpsPending     bool             `json:"currentTurnOutputTpsPending,omitempty"`
	CurrentTurnTimedSamples         int              `json:"currentTurnTimedSamples,omitempty"`
	CurrentTurnDurationSeconds      float64          `json:"currentTurnDurationSeconds,omitempty"`
	CurrentTurnDurationFormatted    string           `json:"currentTurnDurationFormatted,omitempty"`
	CurrentTurnDurationFormattedEn  string           `json:"currentTurnDurationFormattedEn,omitempty"`
	CurrentTurnDurationCompactZh    string           `json:"currentTurnDurationCompactZh,omitempty"`
	CurrentTurnDurationCompactEn    string           `json:"currentTurnDurationCompactEn,omitempty"`
	CurrentTurnModelDurationSeconds float64          `json:"currentTurnModelDurationSeconds,omitempty"`
	CurrentTurnToolDurationSeconds  float64          `json:"currentTurnToolDurationSeconds,omitempty"`
	InFlightModelDurationSeconds    float64          `json:"inFlightModelDurationSeconds,omitempty"`
	ActiveModelStepCount            int              `json:"activeModelStepCount,omitempty"`
	InFlightToolDurationSeconds     float64          `json:"inFlightToolDurationSeconds,omitempty"`
	ActiveToolStepCount             int              `json:"activeToolStepCount,omitempty"`
}

type HudTokenMetrics struct {
	TotalTokens          int64    `json:"totalTokens"`
	TotalTokensCompact   string   `json:"totalTokensCompact"`
	TotalTokensFormatted string   `json:"totalTokensFormatted"`
	CacheHitRate         float64  `json:"cacheHitRate"`
	CacheHitRateFormatted string  `json:"cacheHitRateFormatted"`
	UncachedInputTokens  int64    `json:"uncachedInputTokens"`
	UncachedInputFmt     string   `json:"uncachedInputFormatted"`
	CachedReadTokens     int64    `json:"cachedReadTokens"`
	CachedReadFmt        string   `json:"cachedReadFormatted"`
	CacheWriteTokens     int64    `json:"cacheWriteTokens,omitempty"`
	CacheWriteFmt        string   `json:"cacheWriteFormatted,omitempty"`
	OutputTokens         int64    `json:"outputTokens"`
	OutputFormatted      string   `json:"outputFormatted"`
	ReasoningTokens      int64    `json:"reasoningTokens,omitempty"`
	ReasoningFormatted   string   `json:"reasoningFormatted,omitempty"`
	CandidateTokens      int64    `json:"candidateTokens,omitempty"`
	CandidateFormatted   string   `json:"candidateFormatted,omitempty"`
	EstimatedCostUsd     *float64 `json:"estimatedCostUsd,omitempty"`
	EstimatedCostFmt     string   `json:"estimatedCostFormatted,omitempty"`
	CacheSavedUsd        *float64 `json:"cacheSavedUsd,omitempty"`
	CacheSavedFmt        string   `json:"cacheSavedFormatted,omitempty"`
	PricedStepCount      int      `json:"pricedStepCount,omitempty"`
	HasUnpricedSteps     bool     `json:"hasUnpricedSteps,omitempty"`
}

type HudModelContextStage struct {
	SlotKey             string  `json:"slotKey"`
	Role                string  `json:"role"`
	Section             string  `json:"section,omitempty"`
	ModelID             string  `json:"modelId"`
	ModelDisplayName    string  `json:"modelDisplayName"`
	ShortLabel          string  `json:"shortLabel"`
	CompactLabel        string  `json:"compactLabel"`
	HasUsageData        bool    `json:"hasUsageData"`
	IsActive            bool    `json:"isActive"`
	CompactionCount     int     `json:"compactionCount,omitempty"`
	UsedTokens          int64   `json:"usedTokens"`
	UsedTokensCompact   string  `json:"usedTokensCompact"`
	UsedTokensFormatted string  `json:"usedTokensFormatted"`
	PeakContextTokens   int64   `json:"peakContextTokens"`
	PeakContextCompact  string  `json:"peakContextCompact"`
	PeakContextFmt      string  `json:"peakContextFormatted"`
	CapacityTokens      int64   `json:"capacityTokens"`
	CapacityCompact     string  `json:"capacityTokensCompact"`
	IsFallbackCapacity  bool    `json:"isFallbackCapacity"`
	CapacitySource      string  `json:"capacitySource"`
	UsedRatio           float64 `json:"usedRatio"`
	UsedRatioFormatted  string  `json:"usedRatioFormatted"`
}

type HudContextBreakdown struct {
	CachedContextTokens    int64   `json:"cachedContextTokens"`
	CachedContextFmt       string  `json:"cachedContextFormatted"`
	CachedContextRatio     float64 `json:"cachedContextRatio"`
	UncachedContextTokens  int64   `json:"uncachedContextTokens"`
	UncachedContextFmt     string  `json:"uncachedContextFormatted"`
	UncachedContextRatio   float64 `json:"uncachedContextRatio"`
	LatestOutputTokens     int64   `json:"latestOutputTokens"`
	LatestOutputFmt        string  `json:"latestOutputFormatted"`
	SystemTokens           int64   `json:"systemTokens"`
	SystemTokensCompact    string  `json:"systemTokensCompact"`
	SystemRatio            float64 `json:"systemRatio"`
	ToolTokens             int64   `json:"toolTokens"`
	ToolTokensCompact      string  `json:"toolTokensCompact"`
	ToolRatio              float64 `json:"toolRatio"`
	MessageTokens          int64   `json:"messageTokens"`
	MessageTokensCompact   string  `json:"messageTokensCompact"`
	MessageRatio           float64 `json:"messageRatio"`
}

type HudContextMetrics struct {
	HasUsageData              bool                   `json:"hasUsageData"`
	ModelID                   string                 `json:"modelId"`
	ModelDisplayName          string                 `json:"modelDisplayName"`
	IsMultiModel              bool                   `json:"isMultiModel,omitempty"`
	IsOrchestrator            bool                   `json:"isOrchestrator,omitempty"`
	ModelShortLabel           string                 `json:"modelShortLabel,omitempty"`
	ModelCompactLabel         string                 `json:"modelCompactLabel,omitempty"`
	SessionPeakContextTokens  int64                  `json:"sessionPeakContextTokens,omitempty"`
	ActiveModelCompactionCnt  int                    `json:"activeModelCompactionCount,omitempty"`
	ModelStages               []HudModelContextStage `json:"modelStages,omitempty"`
	UsedTokens                int64                  `json:"usedTokens"`
	UsedTokensCompact         string                 `json:"usedTokensCompact"`
	UsedTokensFormatted       string                 `json:"usedTokensFormatted,omitempty"`
	PeakContextTokens         int64                  `json:"peakContextTokens"`
	PeakContextCompact        string                 `json:"peakContextCompact,omitempty"`
	PeakContextFormatted      string                 `json:"peakContextFormatted,omitempty"`
	CapacityTokens            int64                  `json:"capacityTokens"`
	CapacityTokensCompact     string                 `json:"capacityTokensCompact"`
	HasExplicitDbCapacity     bool                   `json:"hasExplicitDbCapacity,omitempty"`
	IsFallbackCapacity        bool                   `json:"isFallbackCapacity,omitempty"`
	CapacitySource            string                 `json:"capacitySource,omitempty"`
	UsedRatio                 float64                `json:"usedRatio"`
	UsedRatioFormatted        string                 `json:"usedRatioFormatted"`
	StepCacheRatio            float64                `json:"stepCacheRatio,omitempty"`
	StepCacheRatioFormatted   string                 `json:"stepCacheRatioFormatted,omitempty"`
	Breakdown                 HudContextBreakdown    `json:"breakdown"`
}

type MetricsQuery struct {
	ConversationID       string
	PaneSlotID           string
	SessionRevision      int64
	MetricsSequence      int64
	ModelID              string
	IsStreaming          bool
	ActivityPhase        string
	IsSubagentRunning    bool
	RunningSubagentCount int
	ChildConversationIDs []string
}

type cachedDbState struct {
	fileSig              string
	observedAtMs         int64
	steps                []ParsedStep
	childConversationIDs []string
	latestGen            *ParsedGenMetadata
	isSubagentTraj       bool
}

// MetricsEngine reads ~/.gemini/antigravity/conversations/<id>.db safely and aggregates HUD metrics.
type MetricsEngine struct {
	conversationsDir string
	summariesDbPath  string
	mu               sync.Mutex
	cache            map[string]*cachedDbState
}

// NewMetricsEngine creates a HUD metrics engine backed by Antigravity's conversation SQLite stores.
func NewMetricsEngine(stateDir string) *MetricsEngine {
	LoadPricingOverrides(stateDir)
	home, _ := os.UserHomeDir()
	convDir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	return &MetricsEngine{
		conversationsDir: convDir,
		summariesDbPath:  filepath.Join(filepath.Dir(convDir), "conversation_summaries.db"),
		cache:            make(map[string]*cachedDbState),
	}
}

func (e *MetricsEngine) resolveDbPath(convID string) string {
	clean := uuidSanitizeRe.ReplaceAllString(convID, "")
	return filepath.Join(e.conversationsDir, clean+".db")
}

// GetConversationMetrics computes live ConversationHudMetrics for one conversation pane.
func (e *MetricsEngine) GetConversationMetrics(ctx context.Context, q MetricsQuery) ConversationHudMetrics {
	nowMs := time.Now().UnixMilli()
	nowSec := float64(nowMs) / 1000.0
	if q.SessionRevision < 1 {
		q.SessionRevision = 1
	}

	mainState := e.readDbState(ctx, e.resolveDbPath(q.ConversationID))
	if mainState == nil {
		avail := "unavailable"
		if q.IsStreaming || q.IsSubagentRunning {
			avail = "ready"
		}
		return buildMetrics(q, nowMs, nowSec, avail, nil, nil, nil, nil, false, 0)
	}

	// Collect unique child conversation IDs (steps + UI DOM hints).
	childSet := make(map[string]bool)
	mainLow := strings.ToLower(q.ConversationID)
	for _, cid := range mainState.childConversationIDs {
		if cid != "" && cid != mainLow {
			childSet[cid] = true
		}
	}
	for _, cid := range q.ChildConversationIDs {
		low := strings.ToLower(strings.TrimSpace(cid))
		if exactUUIDRe.MatchString(low) && low != mainLow {
			childSet[low] = true
		}
	}

	var descendants []ParsedStep
	failedChildren := 0
	visited := map[string]bool{mainLow: true}
	var walkChildren func(ids []string, depth int)
	walkChildren = func(ids []string, depth int) {
		if depth > 3 {
			return
		}
		for _, cid := range ids {
			if visited[cid] {
				continue
			}
			visited[cid] = true
			cs := e.readDbState(ctx, e.resolveDbPath(cid))
			if cs == nil {
				failedChildren++
				continue
			}
			descendants = append(descendants, cs.steps...)
			if len(cs.childConversationIDs) > 0 {
				walkChildren(cs.childConversationIDs, depth+1)
			}
		}
	}
	if len(childSet) > 0 {
		ids := make([]string, 0, len(childSet))
		for cid := range childSet {
			ids = append(ids, cid)
		}
		sort.Strings(ids)
		walkChildren(ids, 1)
	}

	obs := mainState.observedAtMs
	return buildMetrics(q, nowMs, nowSec, "ready", &obs, &obs, mainState.steps, descendants, mainState.isSubagentTraj, failedChildren)
}

func (e *MetricsEngine) readDbState(ctx context.Context, dbPath string) *cachedDbState {
	st, err := os.Stat(dbPath)
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	walSt, _ := os.Stat(dbPath + "-wal")
	walSize := int64(0)
	walMtime := int64(0)
	if walSt != nil {
		walSize = walSt.Size()
		walMtime = walSt.ModTime().UnixNano()
	}
	sig := fmt.Sprintf("%d:%d:%d:%d", st.Size(), st.ModTime().UnixNano(), walSize, walMtime)

	e.mu.Lock()
	if cached, ok := e.cache[dbPath]; ok && cached.fileSig == sig {
		e.mu.Unlock()
		return cached
	}
	e.mu.Unlock()

	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	db, err := sqlite.Open(qctx, dbPath, sqlite.OpenOptions{ReadOnly: true})
	if err != nil {
		return nil
	}
	defer db.Close()
	_, _ = db.Query(qctx, "PRAGMA query_only = ON; PRAGMA busy_timeout = 400")

	// Detect optional columns on steps.
	hasData, hasRender, hasPayload := false, false, false
	if cols, err := db.Query(qctx, "PRAGMA table_info(steps)"); err == nil {
		for _, r := range cols.Values {
			if len(r) < 2 {
				continue
			}
			name, _ := r[1].(string)
			switch name {
			case "data":
				hasData = true
			case "render_info":
				hasRender = true
			case "step_payload":
				hasPayload = true
			}
		}
	}

	selectCols := "idx, step_type, status, CASE WHEN step_type != 101 THEN metadata ELSE NULL END"
	if hasData {
		selectCols += ", CASE WHEN step_type IN (85, 127, 132) THEN substr(data, 1, 65536) ELSE NULL END"
	} else {
		selectCols += ", NULL"
	}
	if hasRender {
		selectCols += ", CASE WHEN step_type IN (85, 127, 132) THEN substr(render_info, 1, 65536) ELSE NULL END"
	} else {
		selectCols += ", NULL"
	}
	if hasPayload {
		selectCols += ", CASE WHEN step_type IN (85, 127, 132) THEN substr(step_payload, 1, 65536) ELSE NULL END"
	} else {
		selectCols += ", NULL"
	}

	rows, err := db.Query(qctx, "SELECT "+selectCols+" FROM steps ORDER BY idx ASC")
	if err != nil {
		return nil
	}
	var steps []ParsedStep
	childSet := make(map[string]bool)
	for _, r := range rows.Values {
		if len(r) < 7 {
			continue
		}
		idx := toInt(r[0])
		stepType := toInt(r[1])
		status := toInt(r[2])
		meta := toBytes(r[3])
		data := toBytes(r[4])
		render := toBytes(r[5])
		payload := toBytes(r[6])
		ps := ParseStepMetadataBlob(idx, stepType, status, meta, data, render, payload)
		for _, cid := range ps.ChildConversationIDs {
			childSet[cid] = true
		}
		steps = append(steps, ps)
	}

	// Check gen_metadata & trajectory_metadata_blob tables if present.
	hasGen, hasMetaBlob := false, false
	if tbls, err := db.Query(qctx, "SELECT name FROM sqlite_master WHERE type='table' AND name IN ('gen_metadata','trajectory_metadata_blob')"); err == nil {
		for _, r := range tbls.Values {
			if len(r) < 1 {
				continue
			}
			name, _ := r[0].(string)
			if name == "gen_metadata" {
				hasGen = true
			} else if name == "trajectory_metadata_blob" {
				hasMetaBlob = true
			}
		}
	}

	var latestGen *ParsedGenMetadata
	if hasGen {
		genByStep := make(map[int]*ParsedGenMetadata)
		genByExec := make(map[string]*ParsedGenMetadata)
		if grows, err := db.Query(qctx, "SELECT idx, data FROM gen_metadata WHERE data IS NOT NULL ORDER BY idx ASC"); err == nil {
			for _, r := range grows.Values {
				if len(r) < 2 {
					continue
				}
				gdata := toBytes(r[1])
				if len(gdata) == 0 {
					continue
				}
				g := ParseGenMetadataBlob(gdata)
				latestGen = &g
				for _, sIdx := range g.StepIndices {
					genByStep[sIdx] = &g
				}
				if g.ExecutionID != "" {
					genByExec[g.ExecutionID] = &g
				}
			}
		}
		for i := range steps {
			if gm, ok := genByStep[steps[i].Idx]; ok {
				steps[i].Generation = gm
			} else if steps[i].ExecutionID != "" {
				if gm, ok := genByExec[steps[i].ExecutionID]; ok {
					steps[i].Generation = gm
				}
			}
		}
	}

	isSubTraj := false
	if hasMetaBlob {
		if brows, err := db.Query(qctx, "SELECT data FROM trajectory_metadata_blob WHERE data IS NOT NULL LIMIT 4"); err == nil {
			for _, r := range brows.Values {
				if len(r) < 1 {
					continue
				}
				b := toBytes(r[0])
				if len(b) > 0 && len(ExtractParentReferences(b)) > 0 {
					isSubTraj = true
					break
				}
			}
		}
	}

	var children []string
	for cid := range childSet {
		children = append(children, cid)
	}

	observedAt := st.ModTime().UnixMilli()
	if walMtime > 0 && walSt.ModTime().UnixMilli() > observedAt {
		observedAt = walSt.ModTime().UnixMilli()
	}

	res := &cachedDbState{
		fileSig:              sig,
		observedAtMs:         observedAt,
		steps:                steps,
		childConversationIDs: children,
		latestGen:            latestGen,
		isSubagentTraj:       isSubTraj,
	}

	e.mu.Lock()
	if len(e.cache) >= 96 {
		for k := range e.cache {
			delete(e.cache, k)
			break
		}
	}
	e.cache[dbPath] = res
	e.mu.Unlock()

	return res
}

type timeInterval [2]float64

func mergeDurationIntervals(intervals []timeInterval) float64 {
	if len(intervals) == 0 {
		return 0
	}
	sorted := make([]timeInterval, len(intervals))
	copy(sorted, intervals)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i][0] != sorted[j][0] {
			return sorted[i][0] < sorted[j][0]
		}
		return sorted[i][1] < sorted[j][1]
	})
	total := 0.0
	curS, curE := sorted[0][0], sorted[0][1]
	for _, iv := range sorted[1:] {
		if iv[0] <= curE {
			if iv[1] > curE {
				curE = iv[1]
			}
		} else {
			if curE > curS {
				total += curE - curS
			}
			curS, curE = iv[0], iv[1]
		}
	}
	if curE > curS {
		total += curE - curS
	}
	return total
}

func isStepCompleted(s ParsedStep) bool {
	if s.Status == 3 || s.Status == 5 {
		return true
	}
	if activeStepStatuses[s.Status] || s.Status == 4 || s.Status == 6 || s.Status == 7 {
		return false
	}
	return s.EndTimeSeconds != nil
}

func buildMetrics(
	q MetricsQuery,
	nowMs int64,
	nowSec float64,
	availability string,
	observedAt *int64,
	lastSuccessAt *int64,
	mainSteps []ParsedStep,
	childSteps []ParsedStep,
	isSubagentTraj bool,
	failedChildren int,
) ConversationHudMetrics {
	allSteps := make([]ParsedStep, 0, len(mainSteps)+len(childSteps))
	allSteps = append(allSteps, mainSteps...)
	allSteps = append(allSteps, childSteps...)

	var (
		modelSteps, toolSteps        int
		mainCompletedModelDur        float64
		mainModelIntervals           []timeInterval
		childModelIntervals          []timeInterval
		unanchoredChildModelDur      float64
		completedToolIntervals       []timeInterval
		timedOutTokens, timedOutSec  float64
		timedOutCount                int
		latestStepTps                *struct {
			tps float64
			fmt string
		}
		latestStepTtft *struct {
			sec   float64
			zh, en string
		}
		validTtftSum             float64
		validTtftCount           int
		detectedSubagents        int
		uncachedIn, cachedRead   int64
		cacheWrite, outTok       int64
		reasonTok, candTok       int64
		usageSampleCount         int
		pricedSteps, unpriced    int
		totalUsd, cacheSavedUsd  float64
	)

	for i, step := range allSteps {
		isChild := i >= len(mainSteps)
		hasInterval := step.StartTimeSeconds != nil && step.EndTimeSeconds != nil && *step.EndTimeSeconds > *step.StartTimeSeconds
		complete := isStepCompleted(step)
		if step.IsSubagentStep {
			detectedSubagents++
		}
		if step.StepType == 15 {
			modelSteps++
			if step.HasUsage {
				usageSampleCount++
				uncachedIn += step.RawInputTokens
				cachedRead += step.CachedReadTokens
				cacheWrite += step.CacheWriteTokens
				outTok += step.OutputTokens
				reasonTok += step.ReasoningTokens
				candTok += step.CandidateTokens

				modelHint := resolveStepModelHint(step, q.ModelID)
				ts := 0.0
				if step.StartTimeSeconds != nil {
					ts = *step.StartTimeSeconds
				}
				if inR, outR, crR, cwR, ok := LookupPrice(modelHint, ts); ok {
					pricedSteps++
					totalUsd += (float64(step.RawInputTokens)*inR +
						float64(step.CachedReadTokens)*crR +
						float64(step.CacheWriteTokens)*cwR +
						float64(step.OutputTokens)*outR) / 1_000_000.0
					if inR > crR {
						cacheSavedUsd += float64(step.CachedReadTokens) * (inR - crR) / 1_000_000.0
					}
				} else {
					unpriced++
				}
			}

			var dur *float64
			if step.Generation != nil && step.Generation.TimeToFirstTokenSeconds != nil && step.Generation.StreamingDurationSeconds != nil {
				sum := *step.Generation.TimeToFirstTokenSeconds + *step.Generation.StreamingDurationSeconds
				if sum > 0 {
					dur = &sum
				}
			}
			if dur == nil && hasInterval {
				d := *step.EndTimeSeconds - *step.StartTimeSeconds
				dur = &d
			}
			if complete && dur != nil {
				if isChild {
					if step.StartTimeSeconds != nil {
						childModelIntervals = append(childModelIntervals, timeInterval{*step.StartTimeSeconds, *step.StartTimeSeconds + *dur})
					} else {
						unanchoredChildModelDur += *dur
					}
				} else {
					mainCompletedModelDur += *dur
					if step.StartTimeSeconds != nil {
						mainModelIntervals = append(mainModelIntervals, timeInterval{*step.StartTimeSeconds, *step.StartTimeSeconds + *dur})
					}
				}
				if step.HasUsage && *dur > 0 {
					timedOutTokens += float64(step.OutputTokens)
					timedOutSec += *dur
					timedOutCount++
					stepTps := float64(step.OutputTokens) / *dur
					latestStepTps = &struct {
						tps float64
						fmt string
					}{tps: stepTps, fmt: compactRate(stepTps) + " tok/s"}
				}
			}

			var ttft *float64
			if step.Generation != nil && step.Generation.TimeToFirstTokenSeconds != nil {
				ttft = step.Generation.TimeToFirstTokenSeconds
			} else if hasInterval && step.FirstTokenSeconds != nil && *step.FirstTokenSeconds >= *step.StartTimeSeconds && *step.FirstTokenSeconds <= *step.EndTimeSeconds {
				d := *step.FirstTokenSeconds - *step.StartTimeSeconds
				ttft = &d
			}
			if complete && ttft != nil {
				validTtftSum += *ttft
				validTtftCount++
				txt := compactRate(*ttft)
				latestStepTtft = &struct {
					sec    float64
					zh, en string
				}{sec: *ttft, zh: txt + " 秒", en: txt + "s"}
			}
		} else if !nonToolTypes[step.StepType] {
			toolSteps++
			if hasInterval && !activeStepStatuses[step.Status] {
				completedToolIntervals = append(completedToolIntervals, timeInterval{*step.StartTimeSeconds, *step.EndTimeSeconds})
			}
		}
	}

	mainAnchored := mergeDurationIntervals(mainModelIntervals)
	allModelIntervals := append(append([]timeInterval(nil), mainModelIntervals...), childModelIntervals...)
	allAnchored := mergeDurationIntervals(allModelIntervals)
	childDur := math.Max(0, allAnchored-mainAnchored) + unanchoredChildModelDur
	modelDurSec := mainCompletedModelDur + childDur
	toolDurSec := mergeDurationIntervals(completedToolIntervals)

	// Main conversation turn count, compactions, and latest usage step.
	turns := 0
	compactions := 0
	lastTurnIdx := -1
	var latestUsageStep *ParsedStep
	var peakContext int64
	for i := range mainSteps {
		s := &mainSteps[i]
		if s.StepType == 14 {
			turns++
			lastTurnIdx = i
		}
		if s.StepType == 23 && (s.Status == 3 || s.Status == 5) {
			compactions++
		}
		if s.StepType == 15 && s.HasUsage {
			prompt := s.RawInputTokens + s.CachedReadTokens + s.CacheWriteTokens
			if prompt > peakContext {
				peakContext = prompt
			}
			latestUsageStep = s
		}
	}

	// Current turn window analysis.
	var turnSteps []ParsedStep
	if lastTurnIdx >= 0 {
		turnSteps = mainSteps[lastTurnIdx:]
	} else {
		turnSteps = mainSteps
	}
	var turnWallStart, turnCompletedEnd *float64
	var turnModelIvs, turnToolIvs []timeInterval
	var activeModelIvs, activeToolIvs []timeInterval
	var activeModelCnt, activeToolCnt int
	var curTurnOutTok, curTurnOutSec float64
	var curTurnTimed int
	var hasSubInTurn bool
	var lastForegroundKind string
	var detectedRunSub int

	for _, s := range turnSteps {
		if s.StartTimeSeconds != nil {
			if turnWallStart == nil || *s.StartTimeSeconds < *turnWallStart {
				turnWallStart = s.StartTimeSeconds
			}
		}
		if s.EndTimeSeconds != nil && isStepCompleted(s) {
			if turnCompletedEnd == nil || *s.EndTimeSeconds > *turnCompletedEnd {
				turnCompletedEnd = s.EndTimeSeconds
			}
		}
		if s.IsSubagentStep {
			hasSubInTurn = true
		}
		isAct := (q.IsStreaming || activeStepStatuses[s.Status]) && !isStepCompleted(s) && s.Status != 4 && s.Status != 6 && s.Status != 7
		if s.StepType != 14 && activeStepStatuses[s.Status] {
			if s.IsSubagentStep && runningSubagentStatuses[s.Status] {
				detectedRunSub++
			}
			if s.StepType == 15 {
				lastForegroundKind = "model"
			} else if !nonToolTypes[s.StepType] {
				lastForegroundKind = "tool"
			}
		}
		if isAct && s.StartTimeSeconds != nil {
			end := math.Max(*s.StartTimeSeconds, nowSec)
			iv := timeInterval{*s.StartTimeSeconds, end}
			if s.StepType == 15 {
				turnModelIvs = append(turnModelIvs, iv)
				activeModelIvs = append(activeModelIvs, iv)
				activeModelCnt++
			} else if !nonToolTypes[s.StepType] {
				turnToolIvs = append(turnToolIvs, iv)
				activeToolIvs = append(activeToolIvs, iv)
				activeToolCnt++
			}
		} else if s.StepType == 15 && isStepCompleted(s) {
			var dur *float64
			if s.Generation != nil && s.Generation.TimeToFirstTokenSeconds != nil && s.Generation.StreamingDurationSeconds != nil {
				sum := *s.Generation.TimeToFirstTokenSeconds + *s.Generation.StreamingDurationSeconds
				if sum > 0 {
					dur = &sum
				}
			}
			if dur == nil && s.StartTimeSeconds != nil && s.EndTimeSeconds != nil && *s.EndTimeSeconds > *s.StartTimeSeconds {
				d := *s.EndTimeSeconds - *s.StartTimeSeconds
				dur = &d
			}
			if dur != nil && s.StartTimeSeconds != nil {
				turnModelIvs = append(turnModelIvs, timeInterval{*s.StartTimeSeconds, *s.StartTimeSeconds + *dur})
				if s.HasUsage {
					curTurnOutTok += float64(s.OutputTokens)
					curTurnOutSec += *dur
					curTurnTimed++
				}
			}
		} else if !nonToolTypes[s.StepType] && isStepCompleted(s) && s.StartTimeSeconds != nil && s.EndTimeSeconds != nil && *s.EndTimeSeconds > *s.StartTimeSeconds {
			turnToolIvs = append(turnToolIvs, timeInterval{*s.StartTimeSeconds, *s.EndTimeSeconds})
		}
	}

	curTurnModelDur := mergeDurationIntervals(turnModelIvs)
	curTurnToolDur := mergeDurationIntervals(turnToolIvs)
	mergedTurnDur := mergeDurationIntervals(append(append([]timeInterval(nil), turnModelIvs...), turnToolIvs...))
	curTurnDur := mergedTurnDur

	lastAborted := false
	lastClosed := false
	if len(mainSteps) > 0 {
		last := mainSteps[len(mainSteps)-1]
		lastAborted = !q.IsStreaming && last.StepType != 14 && (last.Status == 4 || last.Status == 6 || last.Status == 7)
		lastClosed = last.StepType == 2 && (last.Status == 3 || last.Status == 5)
	}
	if (hasSubInTurn || isSubagentTraj || lastTurnIdx >= 0) && turnWallStart != nil {
		completedSpan := 0.0
		if turnCompletedEnd != nil && *turnCompletedEnd > *turnWallStart {
			completedSpan = *turnCompletedEnd - *turnWallStart
		}
		staleDone := turnCompletedEnd != nil && (nowSec-*turnCompletedEnd) > turnStepHandoffGraceSec
		wallRunning := q.IsSubagentRunning || activeModelCnt > 0 || activeToolCnt > 0 ||
			(q.IsStreaming && !lastAborted && !lastClosed && !staleDone)
		if wallRunning {
			curTurnDur = math.Max(mergedTurnDur, math.Max(nowSec-*turnWallStart, completedSpan))
		} else if completedSpan > 0 {
			curTurnDur = math.Max(mergedTurnDur, completedSpan)
		}
	}

	runSubCount := maxInt(q.RunningSubagentCount, detectedRunSub)
	if q.IsSubagentRunning && runSubCount < 1 {
		runSubCount = 1
	}
	isSubRun := q.IsSubagentRunning || runSubCount > 0
	isStreaming := q.IsStreaming || isSubRun || lastForegroundKind != ""
	phase := "idle"
	if q.IsStreaming || (isSubagentTraj && lastForegroundKind != "") {
		if q.ActivityPhase == "executing" || lastForegroundKind == "tool" {
			phase = "executing"
		} else {
			phase = "generating"
		}
	}

	avgTps := 0.0
	avgTpsFmt := "—"
	tpsPending := timedOutCount == 0 && isStreaming
	if timedOutCount > 0 && timedOutSec > 0 {
		avgTps = timedOutTokens / timedOutSec
		avgTpsFmt = compactRate(avgTps) + " tok/s"
	} else if isStreaming {
		avgTpsFmt = "计算中..."
	}

	curTps := 0.0
	curTpsFmt := "—"
	curTpsPending := curTurnTimed == 0 && isStreaming
	if curTurnTimed > 0 && curTurnOutSec > 0 {
		curTps = curTurnOutTok / curTurnOutSec
		curTpsFmt = compactRate(curTps) + " tok/s"
	} else if isStreaming {
		curTpsFmt = "计算中..."
	}

	avgTtft := 0.0
	avgTtftZh, avgTtftEn := "—", "—"
	if validTtftCount > 0 {
		avgTtft = validTtftSum / float64(validTtftCount)
		rateStr := compactRate(avgTtft)
		avgTtftZh = rateStr + " 秒"
		avgTtftEn = rateStr + "s"
	}

	hasTurnDur := turns > 0 || (len(turnSteps) > 0 && curTurnDur > 0) || isStreaming
	curTurnZh, curTurnEn := "—", "—"
	curTurnCompactZh, curTurnCompactEn := "本轮 0s", "Turn 0s"
	if hasTurnDur {
		curTurnZh = durationText(curTurnDur, "zh")
		curTurnEn = durationText(curTurnDur, "en")
		curTurnCompactZh = formatTurnDurationCompact(curTurnDur, "zh")
		curTurnCompactEn = formatTurnDurationCompact(curTurnDur, "en")
	}

	inFlightModelDur := math.Max(0, mergeDurationIntervals(append(append([]timeInterval(nil), allModelIntervals...), activeModelIvs...))-allAnchored)
	inFlightToolDur := math.Max(0, mergeDurationIntervals(append(append([]timeInterval(nil), completedToolIntervals...), activeToolIvs...))-toolDurSec)

	totalTok := uncachedIn + cachedRead + cacheWrite + outTok
	inTokTotal := uncachedIn + cachedRead + cacheWrite
	cacheHitRate := 0.0
	if inTokTotal > 0 {
		cacheHitRate = float64(cachedRead) / float64(inTokTotal)
	}

	// Context metrics
	rawModelName := q.ModelID
	var explicitCap int64
	if latestUsageStep != nil {
		rawModelName = resolveStepModelHint(*latestUsageStep, q.ModelID)
		if latestUsageStep.Generation != nil && latestUsageStep.Generation.MaxContextTokens > 0 {
			explicitCap = latestUsageStep.Generation.MaxContextTokens
		} else if latestUsageStep.ModelMaxTokens > 0 {
			explicitCap = latestUsageStep.ModelMaxTokens
		}
	}
	spec := ResolveModelCapacity(rawModelName)
	capTokens := spec.CapacityTokens
	capCompact := "~" + spec.CapacityCompact
	capSrc := "reference"
	if spec.IsFallback {
		capSrc = "unknown"
	}
	if explicitCap > 0 {
		capTokens = explicitCap
		capCompact = formatCompactTokenNumber(explicitCap)
		capSrc = "explicit"
	}

	var stepCached, stepUncached, latestOut int64
	if latestUsageStep != nil {
		stepCached = latestUsageStep.CachedReadTokens
		stepUncached = latestUsageStep.RawInputTokens + latestUsageStep.CacheWriteTokens
		latestOut = latestUsageStep.OutputTokens
	}
	usedTok := stepCached + stepUncached
	usedRatio := 0.0
	if capTokens > 0 {
		usedRatio = float64(usedTok) / float64(capTokens)
	}
	stepCacheRatio := 0.0
	if usedTok > 0 {
		stepCacheRatio = float64(stepCached) / float64(usedTok)
	}
	denom := math.Max(float64(capTokens), math.Max(float64(usedTok), 1.0))

	var estCostPtr, cacheSavePtr *float64
	estCostFmt, cacheSaveFmt := "", ""
	if pricedSteps > 0 {
		estCostPtr = &totalUsd
		cacheSavePtr = &cacheSavedUsd
		estCostFmt = compactUsd(totalUsd)
		cacheSaveFmt = compactUsd(cacheSavedUsd)
	}

	var latestStepTpsPtr, latestStepTtftPtr *float64
	var latestStepTpsFmt, latestStepTtftZh, latestStepTtftEn string
	if latestStepTps != nil {
		latestStepTpsPtr = &latestStepTps.tps
		latestStepTpsFmt = latestStepTps.fmt
	}
	if latestStepTtft != nil {
		latestStepTtftPtr = &latestStepTtft.sec
		latestStepTtftZh = latestStepTtft.zh
		latestStepTtftEn = latestStepTtft.en
	}

	upd := nowMs
	if observedAt != nil && *observedAt > 0 {
		upd = *observedAt
	}

	return ConversationHudMetrics{
		ConversationID:       defaultStr(q.ConversationID, "default"),
		PaneSlotID:           q.PaneSlotID,
		SessionRevision:      q.SessionRevision,
		MetricsSequence:      q.MetricsSequence,
		Availability:         availability,
		ObservedAt:           observedAt,
		LastSuccessAt:        lastSuccessAt,
		FailedChildCount:     failedChildren,
		UpdatedAt:            upd,
		IsStreaming:          isStreaming,
		ActivityPhase:        phase,
		IsSubagentRunning:    isSubRun,
		RunningSubagentCount: runSubCount,
		Session: HudSessionMetrics{
			Turns:                           turns,
			Steps:                           len(allSteps),
			ModelSteps:                      modelSteps,
			Samples:                         &HudSampleCounts{Usage: usageSampleCount, Timed: timedOutCount, TTFT: validTtftCount},
			ToolSteps:                       toolSteps,
			CompactionCount:                 compactions,
			SubagentCount:                   maxInt(detectedSubagents, runSubCount),
			RunningSubagentCount:            runSubCount,
			SubagentSteps:                   len(childSteps),
			HasSubagentInTurn:               hasSubInTurn || isSubRun || len(childSteps) > 0,
			IsSubagentTrajectory:            isSubagentTraj,
			TurnAborted:                     lastAborted,
			ModelDurationSeconds:            modelDurSec,
			ModelDurationFormatted:          durationText(modelDurSec, "zh"),
			ModelDurationFormattedEn:        durationText(modelDurSec, "en"),
			ToolDurationSeconds:             toolDurSec,
			ToolDurationFormatted:           durationText(toolDurSec, "zh"),
			ToolDurationFormattedEn:         durationText(toolDurSec, "en"),
			AverageTtftSeconds:              avgTtft,
			AverageTtftFormatted:            avgTtftZh,
			AverageTtftFormattedEn:          avgTtftEn,
			OutputTps:                       avgTps,
			OutputTpsFormatted:              avgTpsFmt,
			OutputTpsPending:                tpsPending,
			LatestStepOutputTps:             latestStepTpsPtr,
			LatestStepOutputTpsFormatted:    latestStepTpsFmt,
			LatestStepTtftSeconds:           latestStepTtftPtr,
			LatestStepTtftFormatted:         latestStepTtftZh,
			LatestStepTtftFormattedEn:       latestStepTtftEn,
			CurrentTurnOutputTps:            curTps,
			CurrentTurnOutputTpsFormatted:   curTpsFmt,
			CurrentTurnOutputTpsPending:     curTpsPending,
			CurrentTurnTimedSamples:         curTurnTimed,
			CurrentTurnDurationSeconds:      curTurnDur,
			CurrentTurnDurationFormatted:    curTurnZh,
			CurrentTurnDurationFormattedEn:  curTurnEn,
			CurrentTurnDurationCompactZh:    curTurnCompactZh,
			CurrentTurnDurationCompactEn:    curTurnCompactEn,
			CurrentTurnModelDurationSeconds: curTurnModelDur,
			CurrentTurnToolDurationSeconds:  curTurnToolDur,
			InFlightModelDurationSeconds:    inFlightModelDur,
			ActiveModelStepCount:            activeModelCnt,
			InFlightToolDurationSeconds:     inFlightToolDur,
			ActiveToolStepCount:             activeToolCnt,
		},
		Tokens: HudTokenMetrics{
			TotalTokens:           totalTok,
			TotalTokensCompact:    formatCompactTokenNumber(totalTok) + " tok",
			TotalTokensFormatted:  formatThousandsTokens(totalTok),
			CacheHitRate:          cacheHitRate,
			CacheHitRateFormatted: compactPercent(cacheHitRate),
			UncachedInputTokens:   uncachedIn,
			UncachedInputFmt:      formatThousandsTokens(uncachedIn),
			CachedReadTokens:      cachedRead,
			CachedReadFmt:         formatThousandsTokens(cachedRead),
			CacheWriteTokens:      cacheWrite,
			CacheWriteFmt:         conditionalThousands(cacheWrite),
			OutputTokens:          outTok,
			OutputFormatted:       formatThousandsTokens(outTok),
			ReasoningTokens:       reasonTok,
			ReasoningFormatted:    conditionalThousands(reasonTok),
			CandidateTokens:       candTok,
			CandidateFormatted:    conditionalThousands(candTok),
			EstimatedCostUsd:      estCostPtr,
			EstimatedCostFmt:      estCostFmt,
			CacheSavedUsd:         cacheSavePtr,
			CacheSavedFmt:         cacheSaveFmt,
			PricedStepCount:       pricedSteps,
			HasUnpricedSteps:      unpriced > 0,
		},
		Context: HudContextMetrics{
			HasUsageData:             latestUsageStep != nil,
			ModelID:                  spec.ModelID,
			ModelDisplayName:         spec.DisplayName,
			ModelShortLabel:          spec.ShortLabel,
			ModelCompactLabel:        spec.CompactLabel,
			SessionPeakContextTokens: peakContext,
			ActiveModelCompactionCnt: compactions,
			UsedTokens:               usedTok,
			UsedTokensCompact:        formatCompactTokenNumber(usedTok),
			UsedTokensFormatted:      formatThousandsTokens(usedTok),
			PeakContextTokens:        peakContext,
			PeakContextCompact:       formatCompactTokenNumber(peakContext),
			PeakContextFormatted:     formatThousandsTokens(peakContext),
			CapacityTokens:           capTokens,
			CapacityTokensCompact:    capCompact,
			HasExplicitDbCapacity:    explicitCap > 0,
			IsFallbackCapacity:       explicitCap <= 0,
			CapacitySource:           capSrc,
			UsedRatio:                usedRatio,
			UsedRatioFormatted:       compactPercent(usedRatio),
			StepCacheRatio:           stepCacheRatio,
			StepCacheRatioFormatted:  compactPercent(stepCacheRatio),
			Breakdown: HudContextBreakdown{
				CachedContextTokens:   stepCached,
				CachedContextFmt:      formatThousandsTokens(stepCached),
				CachedContextRatio:    float64(stepCached) / denom * 100,
				UncachedContextTokens: stepUncached,
				UncachedContextFmt:    formatThousandsTokens(stepUncached),
				UncachedContextRatio:  float64(stepUncached) / denom * 100,
				LatestOutputTokens:    latestOut,
				LatestOutputFmt:       formatThousandsTokens(latestOut),
				SystemTokens:          stepCached,
				SystemTokensCompact:   formatCompactTokenNumber(stepCached) + " tok",
				SystemRatio:           float64(stepCached) / denom * 100,
				ToolTokens:            stepUncached,
				ToolTokensCompact:     formatCompactTokenNumber(stepUncached) + " tok",
				ToolRatio:             float64(stepUncached) / denom * 100,
				MessageTokens:         latestOut,
				MessageTokensCompact:  formatCompactTokenNumber(latestOut) + " tok",
				MessageRatio:          float64(latestOut) / denom * 100,
			},
		},
	}
}

func resolveStepModelHint(s ParsedStep, fallback string) string {
	if s.Generation != nil {
		if s.Generation.ModelDisplayName != "" {
			return s.Generation.ModelDisplayName
		}
		if s.Generation.ResponseModel != "" {
			return s.Generation.ResponseModel
		}
		if s.Generation.PlannerModelName != "" {
			return s.Generation.PlannerModelName
		}
		switch s.Generation.ModelEnum {
		case 1667:
			return "gemini-4-carbon"
		case 1668:
			return "gemini-4-argon"
		case 1669:
			return "gemini-4-hydrogen"
		}
	}
	if s.ModelName != "" {
		return s.ModelName
	}
	return fallback
}

func formatCompactTokenNumber(tokens int64) string {
	if tokens <= 0 {
		return "0"
	}
	if tokens < 1000 {
		return itoa64(tokens)
	}
	v := float64(tokens)
	switch {
	case v >= 1e9:
		return trimSig3(v/1e9) + "B"
	case v >= 1e6:
		res := v / 1e6
		if res >= 999.5 {
			return trimSig3(v/1e9) + "B"
		}
		return trimSig3(res) + "M"
	default:
		res := v / 1e3
		if res >= 999.5 {
			return trimSig3(v/1e6) + "M"
		}
		return trimSig3(res) + "K"
	}
}

func trimSig3(v float64) string {
	s := strconv.FormatFloat(v, 'g', 3, 64)
	if !strings.ContainsAny(s, "eE") {
		return s
	}
	return trimFloat(v, 0)
}

func trimFloat(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func formatThousandsTokens(tokens int64) string {
	if tokens <= 0 {
		return "0 tok"
	}
	raw := itoa64(tokens)
	n := len(raw)
	if n <= 3 {
		return raw + " tok"
	}
	var sb strings.Builder
	rem := n % 3
	if rem > 0 {
		sb.WriteString(raw[:rem])
	}
	for i := rem; i < n; i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(raw[i : i+3])
	}
	sb.WriteString(" tok")
	return sb.String()
}

func conditionalThousands(tokens int64) string {
	if tokens <= 0 {
		return ""
	}
	return formatThousandsTokens(tokens)
}

func compactRate(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return "0"
	}
	if v < 9.995 {
		return trimFloat(v, 2)
	}
	if v < 99.95 {
		return trimFloat(v, 1)
	}
	if v < 999.5 {
		return itoa64(int64(math.Round(v)))
	}
	return formatCompactTokenNumber(int64(math.Round(v)))
}

func compactPercent(ratio float64) string {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
		return "0%"
	}
	pct := ratio * 100
	if pct >= 99.95 {
		return itoa64(int64(math.Round(pct))) + "%"
	}
	if pct >= 1 {
		return trimFloat(pct, 1) + "%"
	}
	return trimFloat(pct, 2) + "%"
}

func compactUsd(usd float64) string {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return "—"
	}
	if usd == 0 {
		return "$0"
	}
	if usd < 0.01 {
		r4 := math.Round(usd*10000) / 10000
		if r4 > 0 {
			return "~$" + trimFloat(usd, 4)
		}
		return "~$" + trimFloat(usd, 6)
	}
	if usd < 999.995 {
		return "~$" + trimFloat(usd, 2)
	}
	return "~$" + formatCompactTokenNumber(int64(math.Round(usd)))
}

func durationText(value float64, lang string) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return "—"
	}
	secs := int64(math.Round(value))
	h := secs / 3600
	m := (secs % 3600) / 60
	r := secs % 60
	if lang == "en" {
		if secs == 0 {
			return "0s"
		}
		var out strings.Builder
		if h > 0 {
			out.WriteString(itoa64(h) + "h")
		}
		if m > 0 {
			out.WriteString(itoa64(m) + "m")
		}
		if r > 0 {
			out.WriteString(itoa64(r) + "s")
		}
		return out.String()
	}
	if secs == 0 {
		return "0秒"
	}
	var out strings.Builder
	if h > 0 {
		out.WriteString(itoa64(h) + "小时")
	}
	if m > 0 {
		out.WriteString(itoa64(m) + "分")
	}
	if r > 0 {
		out.WriteString(itoa64(r) + "秒")
	}
	return out.String()
}

func formatTurnDurationCompact(value float64, lang string) string {
	prefix := "本轮 "
	if lang == "en" {
		prefix = "Turn "
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return prefix + "0s"
	}
	secs := int64(math.Round(value))
	if secs < 60 {
		return prefix + itoa64(secs) + "s"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	if h > 0 {
		if m > 0 {
			return prefix + itoa64(h) + "h" + itoa64(m) + "m"
		}
		return prefix + itoa64(h) + "h"
	}
	return prefix + itoa64(m) + "m"
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func toBytes(v any) []byte {
	switch b := v.(type) {
	case []byte:
		return b
	case string:
		return []byte(b)
	default:
		return nil
	}
}
