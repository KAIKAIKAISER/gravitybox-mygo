package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"gravitybox-mygo/internal/config"
)

// ModelSummary summarizes token, cost, and request stats for one model.
type ModelSummary struct {
	Model        string
	TotalTokens  int64
	RequestCount int64
	EstimatedUSD float64
}

// Summary holds aggregated usage metrics for the Overview and Usage pages.
type Summary struct {
	TodayTokens   int64
	TotalTokens   int64
	TodayUSD      float64
	TotalUSD      float64
	TodayRequests int64
	TotalRequests int64
	TodayHourly   [24]int64
	TodayModels   []ModelSummary
	AllModels     []ModelSummary
}

// LoadSummary reads %APPDATA%\GravityBox\usage-ledger-v2.sqlite (read-only) when present.
func LoadSummary(ctx context.Context) Summary {
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
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()

	rows, err := db.Query(ctx, `SELECT timestamp, model, payload FROM events`)
	if err != nil {
		return out
	}

	todayByModel := map[string]*ModelSummary{}
	allByModel := map[string]*ModelSummary{}

	for _, r := range rows.Values {
		if len(r) < 3 {
			continue
		}
		ts := toInt64(r[0])
		model, _ := r[1].(string)
		payloadStr, _ := r[2].(string)
		if payloadStr == "" {
			continue
		}
		var ev struct {
			Model        string  `json:"model"`
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

		out.TotalTokens += ev.TotalTokens
		out.TotalUSD += ev.EstimatedUSD
		out.TotalRequests++
		am := allByModel[model]
		if am == nil {
			am = &ModelSummary{Model: model}
			allByModel[model] = am
		}
		am.TotalTokens += ev.TotalTokens
		am.EstimatedUSD += ev.EstimatedUSD
		am.RequestCount++

		if ts >= dayStart {
			out.TodayTokens += ev.TotalTokens
			out.TodayUSD += ev.EstimatedUSD
			out.TodayRequests++
			hr := time.Unix(ts, 0).In(now.Location()).Hour()
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
	}

	out.TodayModels = sortModels(todayByModel)
	out.AllModels = sortModels(allByModel)
	return out
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
