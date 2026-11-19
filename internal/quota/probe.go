package quota

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"gravitybox-mygo/internal/win32"
)

var csrfTokenRe = regexp.MustCompile(`(?:^|\s)--csrf_token(?:=|\s+)([^\s"]+)`)

// QuotaWindow represents one displayable quota bar (e.g. "Gemini · 5 小时").
type QuotaWindow struct {
	Label          string
	IconKey        string // "gemini", "claude", "openai"
	RemainingPct   float64
	ResetCountdown string
}

// FamilyQuota summarizes remaining quota for a model family.
type FamilyQuota struct {
	Family        string
	RemainingPct  float64
	HasFiveHour   bool
	FiveHourPct   float64
	FiveHourReset string
	HasWeekly     bool
	WeeklyPct     float64
	WeeklyReset   string
}

// LiveStatus represents live quota and active user info probed from Antigravity's Language Server.
type LiveStatus struct {
	Connected bool
	Email     string
	Name      string
	Tier      string
	Families  []FamilyQuota
	Windows   []QuotaWindow
	Error     string
	ProbedAt  time.Time
}

// ProbeLive queries the running local Antigravity Language Server over loopback HTTPS/HTTP.
func ProbeLive(ctx context.Context) LiveStatus {
	procs, err := win32.FindLanguageServerProcesses(ctx)
	if err != nil || len(procs) == 0 {
		return LiveStatus{Error: "未检测到运行中的 language_server 进程", ProbedAt: time.Now()}
	}
	ports, err := win32.ListListeningPorts(ctx)
	if err != nil {
		return LiveStatus{Error: err.Error(), ProbedAt: time.Now()}
	}

	portsByPID := make(map[uint32][]int)
	for _, lp := range ports {
		portsByPID[lp.PID] = append(portsByPID[lp.PID], lp.Port)
	}

	for _, proc := range procs {
		m := csrfTokenRe.FindStringSubmatch(proc.CommandLine)
		if len(m) < 2 {
			continue
		}
		csrf := m[1]
		for _, port := range portsByPID[proc.PID] {
			if st, ok := probePort(ctx, port, csrf); ok {
				return st
			}
		}
	}
	return LiveStatus{Error: "未能连接本地 Language Server 配额端口", ProbedAt: time.Now()}
}

func probePort(ctx context.Context, port int, csrf string) (LiveStatus, bool) {
	for _, scheme := range []string{"https", "http"} {
		body, err := localRPC(ctx, scheme, port, "/exa.language_server_pb.LanguageServerService/GetUserStatus", csrf, `{}`)
		if err != nil {
			continue
		}
		var parsed struct {
			UserStatus struct {
				Email    string `json:"email"`
				Name     string `json:"name"`
				UserTier struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"userTier"`
				PlanStatus struct {
					PlanInfo struct {
						PlanName string `json:"planName"`
					} `json:"planInfo"`
				} `json:"planStatus"`
				CascadeModelConfigData struct {
					ClientModelConfigs []struct {
						Label     string `json:"label"`
						QuotaInfo *struct {
							RemainingFraction *float64 `json:"remainingFraction"`
							ResetTime         string   `json:"resetTime"`
						} `json:"quotaInfo"`
					} `json:"clientModelConfigs"`
				} `json:"cascadeModelConfigData"`
			} `json:"userStatus"`
		}
		if json.Unmarshal(body, &parsed) != nil || parsed.UserStatus.Email == "" {
			continue
		}

		st := LiveStatus{
			Connected: true,
			Email:     parsed.UserStatus.Email,
			Name:      parsed.UserStatus.Name,
			Tier:      resolveTier(parsed.UserStatus.UserTier.ID, parsed.UserStatus.UserTier.Name, parsed.UserStatus.PlanStatus.PlanInfo.PlanName),
			ProbedAt:  time.Now(),
		}

		if summaryRaw, err := localRPC(ctx, scheme, port, "/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary", csrf, `{}`); err == nil {
			st.Families = parseQuotaSummary(summaryRaw)
		}
		if len(st.Families) == 0 {
			st.Families = parseClientModelQuotas(parsed.UserStatus.CascadeModelConfigData.ClientModelConfigs)
		}
		st.Windows = buildQuotaWindows(st.Families)
		return st, true
	}
	return LiveStatus{}, false
}

func buildQuotaWindows(families []FamilyQuota) []QuotaWindow {
	var out []QuotaWindow
	for _, f := range families {
		icon := "gemini"
		low := strings.ToLower(f.Family)
		if strings.Contains(low, "claude") || strings.Contains(low, "gpt") {
			icon = "claude"
		} else if strings.Contains(low, "openai") {
			icon = "openai"
		}
		if f.HasFiveHour {
			out = append(out, QuotaWindow{
				Label:          f.Family + " · 5 小时",
				IconKey:        icon,
				RemainingPct:   f.FiveHourPct,
				ResetCountdown: FormatCountdown(f.FiveHourReset),
			})
		}
		if f.HasWeekly {
			out = append(out, QuotaWindow{
				Label:          f.Family + " · 每周",
				IconKey:        icon,
				RemainingPct:   f.WeeklyPct,
				ResetCountdown: FormatCountdown(f.WeeklyReset),
			})
		}
		if !f.HasFiveHour && !f.HasWeekly {
			out = append(out, QuotaWindow{
				Label:        f.Family,
				IconKey:      icon,
				RemainingPct: f.RemainingPct,
			})
		}
	}
	return out
}

// FormatCountdown formats an RFC3339 timestamp into Chinese relative countdown text.
func FormatCountdown(resetTime string) string {
	if strings.TrimSpace(resetTime) == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, resetTime)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, resetTime)
		if err != nil {
			return ""
		}
	}
	diff := time.Until(t)
	if diff <= 0 {
		return "即将重置"
	}
	totalMin := int(diff.Minutes())
	days := totalMin / (24 * 60)
	hours := (totalMin % (24 * 60)) / 60
	mins := totalMin % 60
	if days > 0 {
		if hours > 0 {
			return fmt.Sprintf("%d天 %d小时后重置", days, hours)
		}
		return fmt.Sprintf("%d天后重置", days)
	}
	if hours > 0 {
		if mins > 0 {
			return fmt.Sprintf("%d小时 %d分后重置", hours, mins)
		}
		return fmt.Sprintf("%d小时后重置", hours)
	}
	return fmt.Sprintf("%d分钟后重置", max(1, mins))
}

func localRPC(ctx context.Context, scheme string, port int, path, csrf, payload string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
	defer cancel()

	// Strict loopback-only client; local Language Server uses a self-signed cert on 127.0.0.1.
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	client := &http.Client{Transport: tr, Timeout: 1800 * time.Millisecond}
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, port, path), bytes.NewBufferString(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-codeium-csrf-token", csrf)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
}

func resolveTier(tierID, tierName, planName string) string {
	combined := strings.ToLower(tierID + " " + tierName + " " + planName)
	switch {
	case strings.Contains(combined, "ultra"):
		return "ULTRA"
	case strings.Contains(combined, "enterprise"):
		return "ENTERPRISE"
	case strings.Contains(combined, "team") || strings.Contains(combined, "business"):
		return "TEAM"
	case strings.Contains(combined, "plus"):
		return "PLUS"
	case strings.Contains(combined, "pro") || strings.Contains(combined, "ai premium"):
		return "PRO"
	case strings.Contains(combined, "free") || strings.Contains(combined, "starter"):
		return "FREE"
	default:
		return "STANDARD"
	}
}

func parseQuotaSummary(raw []byte) []FamilyQuota {
	var envelope struct {
		Response map[string]any `json:"response"`
	}
	var root map[string]any
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Response) > 0 {
		root = envelope.Response
	} else {
		_ = json.Unmarshal(raw, &root)
	}
	rawGroups, _ := root["quotaGroups"].([]any)
	if len(rawGroups) == 0 {
		return nil
	}
	out := make([]FamilyQuota, 0, len(rawGroups))
	for _, item := range rawGroups {
		gm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := gm["displayName"].(string)
		if name == "" {
			name, _ = gm["display_name"].(string)
		}
		if name == "" {
			continue
		}
		fq := FamilyQuota{Family: name, RemainingPct: 100}
		buckets, _ := gm["buckets"].([]any)
		for _, b := range buckets {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			frac, _ := bm["remainingFraction"].(float64)
			pct := frac * 100
			if pct < 0 {
				pct = 0
			} else if pct > 100 {
				pct = 100
			}
			if pct < fq.RemainingPct {
				fq.RemainingPct = pct
			}
			win, _ := bm["window"].(string)
			reset, _ := bm["resetTime"].(string)
			switch strings.ToLower(win) {
			case "5h", "five_hour":
				fq.HasFiveHour = true
				fq.FiveHourPct = pct
				fq.FiveHourReset = reset
			case "weekly", "7d":
				fq.HasWeekly = true
				fq.WeeklyPct = pct
				fq.WeeklyReset = reset
			}
		}
		out = append(out, fq)
	}
	return out
}

func parseClientModelQuotas(configs []struct {
	Label     string `json:"label"`
	QuotaInfo *struct {
		RemainingFraction *float64 `json:"remainingFraction"`
		ResetTime         string   `json:"resetTime"`
	} `json:"quotaInfo"`
}) []FamilyQuota {
	type agg struct {
		pct   float64
		reset string
	}
	byFam := map[string]agg{}
	for _, c := range configs {
		if c.QuotaInfo == nil || c.QuotaInfo.RemainingFraction == nil {
			continue
		}
		fam := "Gemini"
		low := strings.ToLower(c.Label)
		if strings.Contains(low, "claude") || strings.Contains(low, "gpt") {
			fam = "Claude/GPT"
		} else if strings.Contains(low, "argon") {
			fam = "Gemini 4 Argon"
		}
		pct := *c.QuotaInfo.RemainingFraction * 100
		if cur, ok := byFam[fam]; !ok || pct < cur.pct {
			byFam[fam] = agg{pct: pct, reset: c.QuotaInfo.ResetTime}
		}
	}
	order := []string{"Gemini", "Claude/GPT", "Gemini 4 Argon"}
	var keys []string
	for _, k := range order {
		if _, ok := byFam[k]; ok {
			keys = append(keys, k)
		}
	}
	var extra []string
	for k := range byFam {
		if k != "Gemini" && k != "Claude/GPT" && k != "Gemini 4 Argon" {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	out := make([]FamilyQuota, 0, len(keys))
	for _, k := range keys {
		v := byFam[k]
		out = append(out, FamilyQuota{
			Family:       k,
			RemainingPct: v.pct,
			HasWeekly:    v.reset != "",
			WeeklyPct:    v.pct,
			WeeklyReset:  v.reset,
		})
	}
	return out
}
