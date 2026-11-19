package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/resources"
)

// ModelSummary summarizes token, cost, and request stats for one model.
type ModelSummary struct {
	Model        string
	TotalTokens  int64
	RequestCount int64
	EstimatedUSD float64
}

// ChartBucket represents one bar in the usage distribution chart.
type ChartBucket struct {
	Label  string
	Tokens int64
}

// EventRecord represents a single recent API usage event for the request details dialog.
type EventRecord struct {
	Timestamp    int64
	Model        string
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
	TotalTokens  int64
	EstimatedUSD float64
}

// PriceEntry describes one model's token pricing per 1M tokens.
type PriceEntry struct {
	Model      string  `json:"model"`
	InputPerM  float64 `json:"input"`
	OutputPerM float64 `json:"output"`
	CacheReadM float64 `json:"cache_read"`
}

// Summary holds aggregated usage metrics for both Overview and Usage pages.
type Summary struct {
	// Overview quick stats (all accounts, today vs total)
	TodayTokens   int64
	TotalTokens   int64
	TodayUSD      float64
	TotalUSD      float64
	TodayRequests int64
	TotalRequests int64
	TodayHourly   [24]int64
	TodayModels   []ModelSummary

	// Filtered Usage page stats (by period + accountFilter)
	PeriodTokens    int64
	PeriodInput     int64
	PeriodCached    int64
	PeriodOutput    int64
	PeriodUSD       float64
	PeriodRequests  int64
	AvgTokensPerReq int64
	CacheHitRate    float64
	Buckets         []ChartBucket
	Models          []ModelSummary
	RecentEvents    []EventRecord
}

// LoadSummary reads %APPDATA%\GravityBox\usage-ledger-v2.sqlite (read-only) with optional period/account filter.
// period: 0=Today, 1=Last 7 Days, 2=Last 30 Days, 3=All Time.
func LoadSummary(ctx context.Context, period int, accountFilter string) Summary {
	var out Summary
	dbPath := filepath.Join(config.StateDir(), "usage-ledger-v2.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		return out
	}

	db, err := sqlite.Open(ctx, dbPath, sqlite.OpenOptions{ReadOnly: true})
	if err != nil {
		return out
	}
	defer db.Close()

	now := time.Now()
	loc := now.Location()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).Unix()
	var periodStart int64
	switch period {
	case 0:
		periodStart = dayStart
	case 1:
		periodStart = time.Date(now.Year(), now.Month(), now.Day()-6, 0, 0, 0, 0, loc).Unix()
	case 2:
		periodStart = time.Date(now.Year(), now.Month(), now.Day()-29, 0, 0, 0, 0, loc).Unix()
	default:
		periodStart = 0
	}

	rows, err := db.Query(ctx, `SELECT timestamp, account_id, model, payload FROM events ORDER BY timestamp DESC`)
	if err != nil {
		return out
	}

	todayByModel := map[string]*ModelSummary{}
	periodByModel := map[string]*ModelSummary{}
	dailyBuckets := map[string]int64{}

	for _, r := range rows.Values {
		if len(r) < 4 {
			continue
		}
		ts := toInt64(r[0])
		acctID, _ := r[1].(string)
		model, _ := r[2].(string)
		payloadStr, _ := r[3].(string)
		if payloadStr == "" {
			continue
		}
		var ev struct {
			Model        string  `json:"model"`
			AccountEmail string  `json:"account_email"`
			InputTokens  int64   `json:"input_tokens"`
			CachedTokens int64   `json:"cached_tokens"`
			OutputTokens int64   `json:"output_tokens"`
			TotalTokens  int64   `json:"total_tokens"`
			EstimatedUSD float64 `json:"estimated_usd"`
		}
		if json.Unmarshal([]byte(payloadStr), &ev) != nil {
			continue
		}
		if model == "" {
			model = ev.Model
		}
		if model == "" {
			model = "unknown"
		}

		// Overview (global today & total)
		out.TotalTokens += ev.TotalTokens
		out.TotalUSD += ev.EstimatedUSD
		out.TotalRequests++
		if ts >= dayStart {
			out.TodayTokens += ev.TotalTokens
			out.TodayUSD += ev.EstimatedUSD
			out.TodayRequests++
			hr := time.Unix(ts, 0).In(loc).Hour()
			if hr >= 0 && hr < 24 {
				out.TodayHourly[hr] += ev.TotalTokens
			}
			tm := todayByModel[model]
			if tm == nil {
				tm = &ModelSummary{Model: model}
				todayByModel[model] = tm
			}
			tm.TotalTokens += ev.TotalTokens
			tm.EstimatedUSD += ev.EstimatedUSD
			tm.RequestCount++
		}

		// Filtered Usage view
		if accountFilter != "" && accountFilter != "all" {
			if acctID != accountFilter && ev.AccountEmail != accountFilter {
				continue
			}
		}
		if ts < periodStart {
			continue
		}

		out.PeriodTokens += ev.TotalTokens
		out.PeriodInput += ev.InputTokens
		out.PeriodCached += ev.CachedTokens
		out.PeriodOutput += ev.OutputTokens
		out.PeriodUSD += ev.EstimatedUSD
		out.PeriodRequests++

		pm := periodByModel[model]
		if pm == nil {
			pm = &ModelSummary{Model: model}
			periodByModel[model] = pm
		}
		pm.TotalTokens += ev.TotalTokens
		pm.EstimatedUSD += ev.EstimatedUSD
		pm.RequestCount++

		if period != 0 {
			dayKey := time.Unix(ts, 0).In(loc).Format("01-02")
			dailyBuckets[dayKey] += ev.TotalTokens
		}
		if len(out.RecentEvents) < 80 {
			out.RecentEvents = append(out.RecentEvents, EventRecord{
				Timestamp:    ts,
				Model:        model,
				InputTokens:  ev.InputTokens,
				CachedTokens: ev.CachedTokens,
				OutputTokens: ev.OutputTokens,
				TotalTokens:  ev.TotalTokens,
				EstimatedUSD: ev.EstimatedUSD,
			})
		}
	}

	out.TodayModels = sortModels(todayByModel)
	out.Models = sortModels(periodByModel)
	if out.PeriodRequests > 0 {
		out.AvgTokensPerReq = out.PeriodTokens / out.PeriodRequests
	}
	denom := out.PeriodInput + out.PeriodCached
	if denom > 0 {
		out.CacheHitRate = float64(out.PeriodCached) * 100 / float64(denom)
	}

	if period == 0 {
		// 24h buckets for Today
		var hourly [24]int64
		for _, ev := range out.RecentEvents {
			_ = ev
		}
		// Re-use TodayHourly if all accounts, else recompute from buckets:
		if accountFilter == "" || accountFilter == "all" {
			hourly = out.TodayHourly
		} else {
			for _, r := range rows.Values {
				if len(r) < 4 {
					continue
				}
				ts := toInt64(r[0])
				if ts < dayStart {
					continue
				}
				acctID, _ := r[1].(string)
				payloadStr, _ := r[3].(string)
				var ev struct {
					AccountEmail string `json:"account_email"`
					TotalTokens  int64  `json:"total_tokens"`
				}
				if json.Unmarshal([]byte(payloadStr), &ev) != nil {
					continue
				}
				if acctID != accountFilter && ev.AccountEmail != accountFilter {
					continue
				}
				hr := time.Unix(ts, 0).In(loc).Hour()
				if hr >= 0 && hr < 24 {
					hourly[hr] += ev.TotalTokens
				}
			}
		}
		out.Buckets = make([]ChartBucket, 24)
		for h := 0; h < 24; h++ {
			out.Buckets[h] = ChartBucket{Label: fmt.Sprintf("%02d时", h), Tokens: hourly[h]}
		}
	} else {
		days := 7
		if period == 2 {
			days = 30
		} else if period == 3 {
			days = min(30, max(7, len(dailyBuckets)))
		}
		out.Buckets = make([]ChartBucket, 0, days)
		for i := days - 1; i >= 0; i-- {
			d := now.AddDate(0, 0, -i).Format("01-02")
			out.Buckets = append(out.Buckets, ChartBucket{Label: d, Tokens: dailyBuckets[d]})
		}
	}
	return out
}

// LoadPricingTable reads embedded or remote-synced model pricing entries.
func LoadPricingTable() []PriceEntry {
	var raw []byte
	if b, err := os.ReadFile(filepath.Join(config.StateDir(), "remote_pricing.json")); err == nil {
		raw = b
	} else if b, err := resources.ReadFile("assets/pricing-feed.json"); err == nil {
		raw = b
	}
	if len(raw) == 0 {
		return nil
	}
	var feed struct {
		Models map[string]struct {
			InputPerM     float64 `json:"input_per_million"`
			OutputPerM    float64 `json:"output_per_million"`
			CacheReadPerM float64 `json:"cache_read_per_million"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &feed) != nil || len(feed.Models) == 0 {
		return nil
	}
	out := make([]PriceEntry, 0, len(feed.Models))
	for name, p := range feed.Models {
		out = append(out, PriceEntry{
			Model:      name,
			InputPerM:  p.InputPerM,
			OutputPerM: p.OutputPerM,
			CacheReadM: p.CacheReadPerM,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// CheckRemotePricing fetches the latest public pricing feed from upstream GitHub.
func CheckRemotePricing(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet,
		"https://raw.githubusercontent.com/KAIKAIKAISER/gravitybox/main/assets/pricing-feed.json", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", err
	}
	var check map[string]any
	if json.Unmarshal(raw, &check) != nil {
		return "", fmt.Errorf("定价文件格式无效")
	}
	if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(config.StateDir(), "remote_pricing.json"), raw, 0o600); err != nil {
		return "", err
	}
	return "已同步最新模型价格基准", nil
}

// ExportJSON writes the filtered usage summary to %APPDATA%\GravityBox\reports\ and returns the file path.
func ExportJSON(s Summary) (string, error) {
	dir := filepath.Join(config.StateDir(), "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("usage-export-%s.json", time.Now().Format("20060102-150405")))
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o600)
}

func sortModels(m map[string]*ModelSummary) []ModelSummary {
	res := make([]ModelSummary, 0, len(m))
	for _, v := range m {
		res = append(res, *v)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].TotalTokens > res[j].TotalTokens
	})
	return res
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	default:
		return 0
	}
}

// FormatTokens formats token counts into K/M/B strings matching upstream.
func FormatTokens(v int64) string {
	switch {
	case v >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(v)/1e9)
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(v)/1e6)
	case v >= 1_000:
		return fmt.Sprintf("%.1fK", float64(v)/1e3)
	default:
		return fmt.Sprintf("%d", v)
	}
}

// FormatUSD formats USD estimate cleanly.
func FormatUSD(v float64) string {
	if v <= 0 {
		return "$0.00"
	}
	if v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}
