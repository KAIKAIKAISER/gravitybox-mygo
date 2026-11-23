package hud

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CodexTurnSummary mirrors buildTurnSummaries output in core/daemon/telemetry-renderer.mjs.
type CodexTurnSummary struct {
	TurnIndex    int     `json:"turnIndex"`
	StartStepIdx int     `json:"startStepIdx"`
	EndStepIdx   int     `json:"endStepIdx"`
	MinGenIdx    int     `json:"minGenIdx"`
	MaxGenIdx    int     `json:"maxGenIdx"`
	Steps        int     `json:"steps"`
	Model        string  `json:"model"`
	LastCtx      int64   `json:"lastCtx"`
	CtxStr       string  `json:"ctxStr"`
	PromptTok    int64   `json:"promptTok"`
	PromptStr    string  `json:"promptStr"`
	OutTok       int64   `json:"outTok"`
	OutStr       string  `json:"outStr"`
	InTok        int64   `json:"inTok"`
	CacheWrite   int64   `json:"cacheWrite"`
	CacheRead    int64   `json:"cacheRead"`
	ThinkTok     int64   `json:"thinkTok"`
	RespTok      int64   `json:"respTok"`
	StreamDur    float64 `json:"streamDur"`
	TotDur       float64 `json:"totDur"`
	Tps          int64   `json:"tps"`
	E2eTps       float64 `json:"e2eTps"`
	ContextLimit int64   `json:"contextLimit"`
	LimitStr     string  `json:"limitStr"`
	CtxPct       int     `json:"ctxPct"`
	CacheRate    string  `json:"cacheRate"`
	BadgeText    string  `json:"badgeText"`
	BadgeHTML    string  `json:"badgeHtml"`
	BadgeTitle   string  `json:"badgeTitle"`
}

var orchestratorModelRe = regexp.MustCompile(`(?i)^Orchestrator\b`)
var orchestratorSuffixRe = regexp.MustCompile(`(?i)\s*\(Orchestrator\)\s*$`)

// CodexFmtTok formats token counts matching antigravity-codex-theme fmtTok.
func CodexFmtTok(n int64) string {
	if n <= 0 {
		return "0"
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000.0)
	}
	if n >= 10_000 {
		return strconv.FormatInt(int64(math.Round(float64(n)/1000.0)), 10) + "k"
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return strconv.FormatInt(n, 10)
}

// CodexFmtNum formats an integer with en-US thousands commas.
func CodexFmtNum(n int64) string {
	if n < 0 {
		return "-" + CodexFmtNum(-n)
	}
	raw := strconv.FormatInt(n, 10)
	if len(raw) <= 3 {
		return raw
	}
	var sb strings.Builder
	rem := len(raw) % 3
	if rem > 0 {
		sb.WriteString(raw[:rem])
	}
	for i := rem; i < len(raw); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(raw[i : i+3])
	}
	return sb.String()
}

// CodexFormatContextLimitLabel formats context window limits like 256k / 1M.
func CodexFormatContextLimitLabel(tokens int64) string {
	if tokens <= 0 {
		return "0k"
	}
	if tokens >= 1_000_000 {
		if tokens == 1_000_000 || tokens == 1_048_576 {
			return "1M"
		}
		s := fmt.Sprintf("%.2f", float64(tokens)/1_000_000.0)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		return s + "M"
	}
	return strconv.FormatInt(int64(math.Round(float64(tokens)/1000.0)), 10) + "k"
}

// CodexComputeSegPcts computes visible-min-guaranteed segment percentages summing up to targetTotalPct.
func CodexComputeSegPcts(values []float64, targetTotalPct, minVisiblePct float64) []float64 {
	sum := 0.0
	for _, v := range values {
		if v > 0 {
			sum += v
		}
	}
	clampedTotal := math.Max(0, math.Min(100, targetTotalPct))
	out := make([]float64, len(values))
	if sum <= 0 || clampedTotal <= 0 {
		return out
	}
	var nonZeroIdxs []int
	for i, v := range values {
		if v > 0 {
			out[i] = (v / sum) * clampedTotal
			nonZeroIdxs = append(nonZeroIdxs, i)
		}
	}
	if len(nonZeroIdxs) > 1 && clampedTotal > minVisiblePct*float64(len(nonZeroIdxs)) {
		deficit := 0.0
		flexibleSum := 0.0
		for _, idx := range nonZeroIdxs {
			if out[idx] < minVisiblePct {
				deficit += minVisiblePct - out[idx]
				out[idx] = minVisiblePct
			} else {
				flexibleSum += out[idx] - minVisiblePct
			}
		}
		if deficit > 0 && flexibleSum > 0 {
			ratio := math.Min(1, deficit/flexibleSum)
			for _, idx := range nonZeroIdxs {
				if out[idx] > minVisiblePct {
					out[idx] -= (out[idx] - minVisiblePct) * ratio
				}
			}
		}
	}
	for i := range out {
		out[i] = math.Round(out[i]*10) / 10
	}
	return out
}

// CodexEscapeHTML escapes &, <, >, " identically to escapeHtml in telemetry-renderer.mjs.
func CodexEscapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

func fmtPct1(val float64) string {
	return fmt.Sprintf("%.1f", math.Round(val*10)/10)
}

type inlineTelemetryParams struct {
	TelemetryLayout    string
	TelemetryCacheMode string
	StatusDotHTML      string
	DisplayTps         float64
	TTFT               float64
	LastCtx            int64
	LimitKStr          string
	CtxClass           string
	CtxPct             int
	CompactedPending   bool
	OutTok             int64
	LastCacheRate      string
	SessionCacheRate   string
}

// BuildCodexInlineTelemetryHTML builds the inline pill HTML for compact or detailed layouts.
func BuildCodexInlineTelemetryHTML(p inlineTelemetryParams) string {
	if p.TelemetryLayout == "detailed" {
		inlineCtxHTML := fmt.Sprintf(`<span>Ctx %s/%s <span class="%s">(%d%%)</span></span>`,
			CodexFmtTok(p.LastCtx), p.LimitKStr, p.CtxClass, p.CtxPct)
		if p.CompactedPending {
			inlineCtxHTML = `<span class="ag-ctx-compacted">Ctx 已压缩</span>`
		}
		return p.StatusDotHTML +
			fmt.Sprintf(`<span>%d tok/s</span>`, int64(math.Round(p.DisplayTps))) +
			`<span class="ag-sep">·</span>` +
			fmt.Sprintf(`<span>TTFT %.1fs</span>`, p.TTFT) +
			`<span class="ag-sep">·</span>` +
			inlineCtxHTML +
			`<span class="ag-sep">·</span>` +
			fmt.Sprintf(`<span>Out %s</span>`, CodexFmtTok(p.OutTok))
	}

	cacheVal := p.SessionCacheRate
	if p.TelemetryCacheMode == "step" {
		cacheVal = p.LastCacheRate
	}
	compactCtxHTML := fmt.Sprintf(`<span>Ctx <span class="%s">%d%%</span></span>`, p.CtxClass, p.CtxPct)
	if p.CompactedPending {
		compactCtxHTML = `<span class="ag-ctx-compacted">Ctx 已压缩</span>`
	}
	return p.StatusDotHTML +
		fmt.Sprintf(`<span>%d tok/s</span>`, int64(math.Round(p.DisplayTps))) +
		`<span class="ag-sep">·</span>` +
		compactCtxHTML +
		`<span class="ag-sep">·</span>` +
		fmt.Sprintf(`<span>Cache %s%%</span>`, cacheVal)
}

type tooltipParams struct {
	GenIdx             int
	StreamTps          float64
	E2eTps             float64
	StreamTpsValid     bool
	Last               CodexGenItem
	LastCacheRate      string
	CtxPct             int
	ContextLimitTokens int64
	LimitKStr          string
	RemainCtx          int64
	CompactedPending   bool
	CompactSummaryKb   float64
	CompactingNow      bool
	GeneratingPending  bool
	PendingRetrying    bool
	LastErrorMsg       string
	TurnSteps          int
	TurnOut            int64
	TurnThink          int64
	TurnResp           int64
	TurnDur            float64
	UserStepsCount     int
	ValidCount         int
	TotalIn            int64
	TotalCacheWrite    int64
	TotalCache         int64
	TotalPrompt        int64
	SessionCacheRate   string
	TotalOut           int64
	TotalThink         int64
	TotalResp          int64
}

// BuildCodexTooltipHTML builds the L1/L2 Bento tooltip HTML matching buildMetricsTooltipHtml.
func BuildCodexTooltipHTML(p tooltipParams) string {
	ctxLevelClass := "ag-tt-ok"
	if p.CompactedPending {
		ctxLevelClass = "ag-tt-compacted"
	} else if p.CtxPct >= 90 {
		ctxLevelClass = "ag-tt-crit"
	} else if p.CtxPct >= 75 {
		ctxLevelClass = "ag-tt-warn"
	}

	mainTpsVal := p.E2eTps
	speedSub := "端到端均值"
	if p.StreamTpsValid {
		mainTpsVal = p.StreamTps
		speedSub = fmt.Sprintf("端到端 %.1f", p.E2eTps)
	}

	parsedSessCache, _ := strconv.ParseFloat(p.SessionCacheRate, 64)
	cacheGoodCls := ""
	if parsedSessCache > 0 {
		cacheGoodCls = " ag-tt-val-good"
	}

	liveChipClass := ""
	liveChipText := ""
	if p.LastErrorMsg != "" && !p.CompactingNow && !p.GeneratingPending && !p.PendingRetrying {
		liveChipClass = " is-error"
		liveChipText = "执行报错"
	} else if p.PendingRetrying {
		liveChipClass = " is-slow"
		liveChipText = "重试连接中…"
	} else if p.CompactingNow {
		liveChipText = "压缩中…"
	} else if p.GeneratingPending {
		liveChipText = "新步执行中…"
	}
	liveChipHTML := fmt.Sprintf(`<span class="ag-tt-live-chip%s">%s</span>`, liveChipClass, liveChipText)

	ctxBentoValHTML := fmt.Sprintf(`<span class="ag-tt-pill">%d%%</span>`, p.CtxPct)
	ctxBentoSubText := fmt.Sprintf("余 %s / %s", CodexFmtTok(p.RemainCtx), p.LimitKStr)
	if p.CompactedPending {
		ctxBentoValHTML = `<span class="ag-tt-pill ag-tt-compacted-val">已压缩</span>`
		if p.GeneratingPending {
			ctxBentoSubText = "新步落盘后刷新"
		} else {
			ctxBentoSubText = "下次调用后刷新"
		}
	}

	ctxUsedTargetPct := 0.0
	if p.ContextLimitTokens > 0 {
		ctxUsedTargetPct = math.Min(100, (float64(p.Last.Ctx)/float64(p.ContextLimitTokens))*100)
	}
	hasStepWrite := p.Last.CacheWrite > 0
	var ctxSegsHTML string
	if p.CompactedPending {
		ctxSegsHTML = `<div class="ag-tt-seg ag-tt-c-emerald" style="width:8%"></div>`
	} else if hasStepWrite {
		ctxPcts := CodexComputeSegPcts([]float64{float64(p.Last.InTok), float64(p.Last.CacheWrite), float64(p.Last.CacheRead)}, ctxUsedTargetPct, 1.5)
		ctxSegsHTML = fmt.Sprintf(`<div class="ag-tt-seg ag-tt-c-ctx-new" style="width:%s%%"></div><div class="ag-tt-seg ag-tt-c-slate" style="width:%s%%"></div><div class="ag-tt-seg ag-tt-c-ctx-cache" style="width:%s%%"></div>`,
			fmtPct1(ctxPcts[0]), fmtPct1(ctxPcts[1]), fmtPct1(ctxPcts[2]))
	} else {
		ctxPcts := CodexComputeSegPcts([]float64{float64(p.Last.InTok), float64(p.Last.CacheRead)}, ctxUsedTargetPct, 1.6)
		ctxSegsHTML = fmt.Sprintf(`<div class="ag-tt-seg ag-tt-c-ctx-new" style="width:%s%%"></div><div class="ag-tt-seg ag-tt-c-ctx-cache" style="width:%s%%"></div>`,
			fmtPct1(ctxPcts[0]), fmtPct1(ctxPcts[1]))
	}

	ctxWriteLeg := ""
	if hasStepWrite {
		ctxWriteLeg = fmt.Sprintf(`<span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-slate"></span>写缓存 %s</span>`, CodexFmtNum(p.Last.CacheWrite))
	}

	var ctxBarTopMetaHTML, ctxBarLegendHTML string
	if p.CompactedPending {
		kbPart := ""
		if p.CompactSummaryKb > 0 {
			kbPart = fmt.Sprintf(" · 摘要 %s KB", trimFloat(p.CompactSummaryKb, 1))
		}
		ctxBarTopMetaHTML = `<strong>已完成压缩</strong>` + kbPart
		ctxBarLegendHTML = fmt.Sprintf(`<span class="ag-tt-legend-left"><span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-emerald"></span>压缩前 %s (%d%%) · 下轮落盘更新精确占比</span></span>`,
			CodexFmtNum(p.Last.Ctx), p.CtxPct)
	} else {
		ctxBarTopMetaHTML = fmt.Sprintf(`<strong>%s</strong> / %s`, CodexFmtNum(p.Last.Ctx), p.LimitKStr)
		ctxBarLegendHTML = fmt.Sprintf(`<span class="ag-tt-legend-left">
          <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-ctx-new"></span>新增 %s</span>
          %s
          <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-ctx-cache"></span>缓存 %s (%s%%)</span>
        </span>
        <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-dot-outline"></span>余 %s</span>`,
			CodexFmtNum(p.Last.InTok), ctxWriteLeg, CodexFmtNum(p.Last.CacheRead), p.LastCacheRate, CodexFmtTok(p.RemainCtx))
	}

	durPcts := CodexComputeSegPcts([]float64{p.Last.TTFT, p.Last.Stream}, 100, 3)
	stepOutPcts := CodexComputeSegPcts([]float64{float64(p.Last.ThinkTok), float64(p.Last.RespTok)}, 100, 3)

	turnBarHTML := ""
	if p.TurnSteps > 1 {
		turnPcts := CodexComputeSegPcts([]float64{float64(p.TurnThink), float64(p.TurnResp)}, 100, 3)
		turnBarHTML = fmt.Sprintf(`<div class="ag-tt-bar-blk ag-tt-none">
          <div class="ag-tt-bar-top">
            <span class="ag-tt-bar-name">本轮合计（%d步 · 耗时 %.1fs）</span>
            <span class="ag-tt-bar-meta">输出 <strong>%s</strong></span>
          </div>
          <div class="ag-tt-track">
            <div class="ag-tt-seg ag-tt-c-indigo" style="width:%s%%"></div>
            <div class="ag-tt-seg ag-tt-c-brand" style="width:%s%%"></div>
          </div>
          <div class="ag-tt-legend">
            <span class="ag-tt-legend-left">
              <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-indigo"></span>思考 %s</span>
              <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-brand"></span>响应 %s</span>
            </span>
          </div>
        </div>`,
			p.TurnSteps, p.TurnDur, CodexFmtNum(p.TurnOut),
			fmtPct1(turnPcts[0]), fmtPct1(turnPcts[1]),
			CodexFmtNum(p.TurnThink), CodexFmtNum(p.TurnResp))
	}

	hasTotalWrite := p.TotalCacheWrite > 0
	var totalInSegsHTML string
	if hasTotalWrite {
		totalInPcts := CodexComputeSegPcts([]float64{float64(p.TotalIn), float64(p.TotalCacheWrite), float64(p.TotalCache)}, 100, 2)
		totalInSegsHTML = fmt.Sprintf(`<div class="ag-tt-seg ag-tt-c-mist" style="width:%s%%"></div><div class="ag-tt-seg ag-tt-c-slate" style="width:%s%%"></div><div class="ag-tt-seg ag-tt-c-emerald" style="width:%s%%"></div>`,
			fmtPct1(totalInPcts[0]), fmtPct1(totalInPcts[1]), fmtPct1(totalInPcts[2]))
	} else {
		totalInPcts := CodexComputeSegPcts([]float64{float64(p.TotalIn), float64(p.TotalCache)}, 100, 2)
		totalInSegsHTML = fmt.Sprintf(`<div class="ag-tt-seg ag-tt-c-mist" style="width:%s%%"></div><div class="ag-tt-seg ag-tt-c-emerald" style="width:%s%%"></div>`,
			fmtPct1(totalInPcts[0]), fmtPct1(totalInPcts[1]))
	}
	totalWriteLeg := ""
	if hasTotalWrite {
		totalWriteLeg = fmt.Sprintf(`<span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-slate"></span>写缓存 %s</span>`, CodexFmtTok(p.TotalCacheWrite))
	}
	totalOutPcts := CodexComputeSegPcts([]float64{float64(p.TotalThink), float64(p.TotalResp)}, 100, 2)

	return fmt.Sprintf(`<div class="ag-tt-wrap %s">
    <div class="ag-tt-hdr">
      <span class="ag-tt-hdr-left">本轮最新调用</span>
      <span class="ag-tt-hdr-right">%s<span class="ag-tt-gen-chip">Gen #%d</span></span>
    </div>
    <div class="ag-tt-wait-note" id="ag-tt-wait-note"></div>
    <div class="ag-tt-bento">
      <div class="ag-tt-bento-cell">
        <div class="ag-tt-bento-lbl">生成速度</div>
        <div class="ag-tt-bento-val-row">
          <span class="ag-tt-bento-val">%.1f</span>
          <span class="ag-tt-bento-unit">t/s</span>
        </div>
        <div class="ag-tt-bento-sub">%s</div>
      </div>
      <div class="ag-tt-bento-cell">
        <div class="ag-tt-bento-lbl">上下文窗口</div>
        <div class="ag-tt-bento-val-row">%s</div>
        <div class="ag-tt-bento-sub">%s</div>
      </div>
      <div class="ag-tt-bento-cell">
        <div class="ag-tt-bento-lbl">累计缓存命中</div>
        <div class="ag-tt-bento-val-row">
          <span class="ag-tt-bento-val%s">%s%%</span>
        </div>
        <div class="ag-tt-bento-sub">本步 %s%%</div>
      </div>
    </div>
    <div class="ag-tt-bar-blk ag-tt-none">
      <div class="ag-tt-bar-top">
        <span class="ag-tt-bar-name">当前上下文</span>
        <span class="ag-tt-bar-meta">%s</span>
      </div>
      <div class="ag-tt-track">%s</div>
      <div class="ag-tt-legend">%s</div>
    </div>
    <div class="ag-tt-l2">
      <div class="ag-tt-grid-2">
        <div class="ag-tt-grid-2-col">
          <div class="ag-tt-bar-top">
            <span class="ag-tt-bar-name">响应耗时</span>
            <span class="ag-tt-bar-meta"><strong>%.2fs</strong></span>
          </div>
          <div class="ag-tt-track">
            <div class="ag-tt-seg ag-tt-c-slate" style="width:%s%%"></div>
            <div class="ag-tt-seg ag-tt-c-brand" style="width:%s%%"></div>
          </div>
          <div class="ag-tt-legend">
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-slate"></span>TTFT %.2fs</span>
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-brand"></span>流式 %.2fs</span>
          </div>
        </div>
        <div class="ag-tt-grid-2-col">
          <div class="ag-tt-bar-top">
            <span class="ag-tt-bar-name">本步输出</span>
            <span class="ag-tt-bar-meta"><strong>%s</strong></span>
          </div>
          <div class="ag-tt-track">
            <div class="ag-tt-seg ag-tt-c-indigo" style="width:%s%%"></div>
            <div class="ag-tt-seg ag-tt-c-brand" style="width:%s%%"></div>
          </div>
          <div class="ag-tt-legend">
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-indigo"></span>思考 %s</span>
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-brand"></span>正文 %s</span>
          </div>
        </div>
      </div>
      %s
      <div class="ag-tt-sec-hdr">
        <span>当前会话累计</span>
        <span class="ag-tt-badge-inline">%d 轮提问 / %d 次有效调用</span>
      </div>
      <div class="ag-tt-bar-blk">
        <div class="ag-tt-bar-top">
          <span class="ag-tt-bar-name">累计输入量</span>
          <span class="ag-tt-bar-meta">总 <strong>%s</strong> · 命中率 %s%%</span>
        </div>
        <div class="ag-tt-track">%s</div>
        <div class="ag-tt-legend">
          <span class="ag-tt-legend-left">
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-mist"></span>新增 %s</span>
            %s
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-emerald"></span>缓存读取 %s</span>
          </span>
        </div>
      </div>
      <div class="ag-tt-bar-blk ag-tt-none">
        <div class="ag-tt-bar-top">
          <span class="ag-tt-bar-name">累计输出量</span>
          <span class="ag-tt-bar-meta">总计 <strong>%s</strong></span>
        </div>
        <div class="ag-tt-track">
          <div class="ag-tt-seg ag-tt-c-indigo" style="width:%s%%"></div>
          <div class="ag-tt-seg ag-tt-c-brand" style="width:%s%%"></div>
        </div>
        <div class="ag-tt-legend">
          <span class="ag-tt-legend-left">
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-indigo"></span>思考链 %s</span>
            <span class="ag-tt-leg"><span class="ag-tt-dot ag-tt-c-brand"></span>正文/工具 %s</span>
          </span>
        </div>
      </div>
    </div>
    <div class="ag-tt-footer">
      <span class="ag-tt-footer-summary">%d 轮 · %d 次 · 耗时 %.2fs</span>
      <button type="button" class="ag-tt-btn-toggle" data-ag-tt-toggle="1">
        <span>展开明细</span>
        <svg class="ag-tt-chevron" width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M4 6l4 4 4-4"/></svg>
      </button>
    </div>
  </div>`,
		ctxLevelClass,
		liveChipHTML, p.GenIdx,
		mainTpsVal, speedSub,
		ctxBentoValHTML, ctxBentoSubText,
		cacheGoodCls, p.SessionCacheRate, p.LastCacheRate,
		ctxBarTopMetaHTML, ctxSegsHTML, ctxBarLegendHTML,
		p.Last.TotDur,
		fmtPct1(durPcts[0]), fmtPct1(durPcts[1]), p.Last.TTFT, p.Last.Stream,
		CodexFmtNum(p.Last.OutTok),
		fmtPct1(stepOutPcts[0]), fmtPct1(stepOutPcts[1]), CodexFmtNum(p.Last.ThinkTok), CodexFmtNum(p.Last.RespTok),
		turnBarHTML,
		p.UserStepsCount, p.ValidCount,
		CodexFmtTok(p.TotalPrompt), p.SessionCacheRate,
		totalInSegsHTML,
		CodexFmtTok(p.TotalIn), totalWriteLeg, CodexFmtTok(p.TotalCache),
		CodexFmtTok(p.TotalOut),
		fmtPct1(totalOutPcts[0]), fmtPct1(totalOutPcts[1]),
		CodexFmtTok(p.TotalThink), CodexFmtTok(p.TotalResp),
		p.UserStepsCount, p.ValidCount, p.Last.TotDur,
	)
}

// FormatTurnModelName formats model name transitions within a turn.
func FormatTurnModelName(gens []CodexGenItem) string {
	var models []string
	seen := make(map[string]bool)
	for _, g := range gens {
		if g.ModelName != "" && !seen[g.ModelName] {
			seen[g.ModelName] = true
			models = append(models, g.ModelName)
		}
	}
	if len(models) == 0 {
		return ""
	}
	if len(models) == 1 {
		return models[0]
	}
	first := models[0]
	last := models[len(models)-1]
	if orchestratorModelRe.MatchString(first) {
		last = strings.TrimSpace(orchestratorSuffixRe.ReplaceAllString(last, ""))
	}
	if last != "" && last != first {
		return first + " → " + last
	}
	return first
}

// BuildCodexTurnSummaries groups valid generations by user step buckets identically to buildTurnSummaries.
func BuildCodexTurnSummaries(valid []CodexGenItem, userStepIdxs []int, manualLim, fallbackLim int64) []CodexTurnSummary {
	if len(valid) == 0 {
		return []CodexTurnSummary{}
	}
	hasStepIdx := false
	for _, it := range valid {
		if it.MaxStepIdx >= 0 {
			hasStepIdx = true
			break
		}
	}
	if !hasStepIdx {
		return []CodexTurnSummary{}
	}

	buckets := make(map[int][]CodexGenItem)
	for _, item := range valid {
		si := item.MaxStepIdx
		if si < 0 {
			continue
		}
		lo, hi := 0, len(userStepIdxs)-1
		foundIdx := -1
		for lo <= hi {
			mid := (lo + hi) >> 1
			if userStepIdxs[mid] <= si {
				foundIdx = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		buckets[foundIdx] = append(buckets[foundIdx], item)
	}

	keys := make([]int, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	const minStreamDurSec = 0.12
	const maxSanityStreamTps = 3000.0
	turns := make([]CodexTurnSummary, 0, len(keys))

	for _, k := range keys {
		gens := buckets[k]
		if len(gens) == 0 {
			continue
		}
		startStepIdx := -1
		if k >= 0 && k < len(userStepIdxs) {
			startStepIdx = userStepIdxs[k]
		}
		endStepIdx := -1
		if k+1 < len(userStepIdxs) {
			endStepIdx = userStepIdxs[k+1] - 1
		}

		var inTokSum, cacheWriteSum, cacheReadSum, outTokSum, thinkTokSum, respTokSum int64
		var streamSum, totDurSum float64
		var nativeMaxCtx int64
		for _, g := range gens {
			inTokSum += g.InTok
			cacheWriteSum += g.CacheWrite
			cacheReadSum += g.CacheRead
			outTokSum += g.OutTok
			thinkTokSum += g.ThinkTok
			respTokSum += g.RespTok
			streamSum += g.Stream
			totDurSum += g.TotDur
			if g.MaxCtx > 0 {
				nativeMaxCtx = g.MaxCtx
			}
		}

		firstG := gens[0]
		lastG := gens[len(gens)-1]
		lastCtx := lastG.Ctx
		contextLimit := fallbackLim
		if manualLim > 0 {
			contextLimit = manualLim
		} else if nativeMaxCtx > 0 {
			contextLimit = nativeMaxCtx
		}
		if contextLimit <= 0 {
			contextLimit = 256000
		}

		streamTps := 0.0
		if streamSum >= minStreamDurSec {
			streamTps = float64(outTokSum) / streamSum
		}
		e2eTps := 0.0
		if totDurSum > 0 {
			e2eTps = float64(outTokSum) / totDurSum
		}
		rawTps := e2eTps
		if streamTps > 0 && streamTps <= maxSanityStreamTps {
			rawTps = streamTps
		}
		tps := int64(math.Round(rawTps))

		totalPromptTurn := inTokSum + cacheWriteSum + cacheReadSum
		model := FormatTurnModelName(gens)
		cacheRate := "0.0%"
		if totalPromptTurn > 0 {
			cacheRate = fmt.Sprintf("%.1f%%", (float64(cacheReadSum)/float64(totalPromptTurn))*100)
		}
		ctxStr := CodexFmtTok(lastCtx)
		outStr := CodexFmtTok(outTokSum)
		limitStr := CodexFormatContextLimitLabel(contextLimit)
		ctxPct := int(math.Round((float64(lastCtx) / float64(contextLimit)) * 100))

		var badgeParts []string
		if model != "" {
			badgeParts = append(badgeParts, model)
		}
		badgeParts = append(badgeParts, fmt.Sprintf("%d t/s", tps))
		badgeParts = append(badgeParts, "In "+CodexFmtTok(inTokSum+cacheReadSum))
		badgeParts = append(badgeParts, "Out "+outStr)
		badgeParts = append(badgeParts, "Cache "+cacheRate)

		modelHTML := ""
		if model != "" {
			modelHTML = `<span class="ag-turn-model">` + CodexEscapeHTML(model) + `</span><span class="ag-sep">·</span>`
		}
		badgeHTML := modelHTML +
			fmt.Sprintf(`<span>%d t/s</span><span class="ag-sep">·</span>`, tps) +
			fmt.Sprintf(`<span>Out %s</span><span class="ag-sep">·</span>`, outStr) +
			fmt.Sprintf(`<span>Cache %s</span>`, cacheRate)

		badgeTitle := fmt.Sprintf("Ctx %s/%s (%d%%) · %d 步 · 耗时 %.1fs", ctxStr, limitStr, ctxPct, len(gens), totDurSum)

		turnIndex := 0
		if k >= 0 {
			turnIndex = k + 1
		}

		turns = append(turns, CodexTurnSummary{
			TurnIndex:    turnIndex,
			StartStepIdx: startStepIdx,
			EndStepIdx:   endStepIdx,
			MinGenIdx:    firstG.Idx,
			MaxGenIdx:    lastG.Idx,
			Steps:        len(gens),
			Model:        model,
			LastCtx:      lastCtx,
			CtxStr:       ctxStr,
			PromptTok:    totalPromptTurn,
			PromptStr:    CodexFmtTok(totalPromptTurn),
			OutTok:       outTokSum,
			OutStr:       outStr,
			InTok:        inTokSum,
			CacheWrite:   cacheWriteSum,
			CacheRead:    cacheReadSum,
			ThinkTok:     thinkTokSum,
			RespTok:      respTokSum,
			StreamDur:    math.Round(streamSum*10) / 10,
			TotDur:       math.Round(totDurSum*10) / 10,
			Tps:          tps,
			E2eTps:       math.Round(e2eTps*10) / 10,
			ContextLimit: contextLimit,
			LimitStr:     limitStr,
			CtxPct:       ctxPct,
			CacheRate:    cacheRate,
			BadgeText:    strings.Join(badgeParts, " · "),
			BadgeHTML:    badgeHTML,
			BadgeTitle:   badgeTitle,
		})
	}
	return turns
}
