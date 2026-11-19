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
	"strings"
	"time"

	"gravitybox-mygo/internal/win32"
)

var csrfTokenRe = regexp.MustCompile(`(?:^|\s)--csrf_token(?:=|\s+)([^\s"]+)`)

// FamilyQuota summarizes remaining quota for a model family.
type FamilyQuota struct {
	Family          string
	RemainingPct    float64
	HasFiveHour     bool
	FiveHourPct     float64
	FiveHourReset   string
	HasWeekly       bool
	WeeklyPct       float64
	WeeklyReset     string
}

// LiveStatus represents live quota and active user info probed from Antigravity's Language Server.
type LiveStatus struct {
	Connected bool
	Email     string
	Name      string
	Tier      string
	Families  []FamilyQuota
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

		// Also try RetrieveUserQuotaSummary for 5h / weekly buckets.
		if summaryRaw, err := localRPC(ctx, scheme, port, "/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary", csrf, `{}`); err == nil {
			st.Families = parseQuotaSummary(summaryRaw)
		}
		if len(st.Families) == 0 {
			st.Families = parseClientModelQuotas(parsed.UserStatus.CascadeModelConfigData.ClientModelConfigs)
		}
		return st, true
	}
	return LiveStatus{}, false
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
	minByFamily := map[string]float64{}
	for _, c := range configs {
		if c.QuotaInfo == nil || c.QuotaInfo.RemainingFraction == nil {
			continue
		}
		fam := "Gemini"
		low := strings.ToLower(c.Label)
		if strings.Contains(low, "claude") || strings.Contains(low, "gpt") {
			fam = "Claude/GPT"
		}
		pct := *c.QuotaInfo.RemainingFraction * 100
		if cur, ok := minByFamily[fam]; !ok || pct < cur {
			minByFamily[fam] = pct
		}
	}
	out := make([]FamilyQuota, 0, len(minByFamily))
	for fam, pct := range minByFamily {
		out = append(out, FamilyQuota{Family: fam, RemainingPct: pct})
	}
	return out
}
