package ui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"

	"gravitybox-mygo/internal/accounts"
	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/quota"
	"gravitybox-mygo/internal/usage"
	"gravitybox-mygo/internal/win32"
)

func (a *App) debugPortBanner(c *mygoui.Context) {
	if !a.antigravityRunning || a.cdpStatus.Mode != "debug-port-missing" {
		return
	}
	t := c.Theme()
	bannerBg := mygoui.RGB(254, 247, 230)
	bannerBorder := mygoui.RGB(245, 208, 128)
	if t.Dark {
		bannerBg = mygoui.RGBA(217, 119, 6, 0.14)
		bannerBorder = mygoui.RGBA(245, 158, 11, 0.35)
	}
	mygoui.Row(c).Padding(14, 16).Gap(14).Radius(12).AlignItems(mygoui.Center).Background(bannerBg).Border(1, bannerBorder).Children(func() {
		mygoui.Column(c).Grow(1).Gap(4).Children(func() {
			mygoui.Text(c, "未开启调试端口").FontSize(14).Bold()
			mygoui.Text(c, "检测到 Antigravity 已运行但未开启调试端口，汉化与对话状态栏暂不可连接。请先保存工作后再安全重启。").
				FontSize(12).TextColor(t.TextMuted)
		})
		if mygoui.PrimaryButton(c, "安全重启 Antigravity 以连接").Disabled(a.relaunch.Load()).Clicked() {
			a.safeRelaunchAsync()
		}
	})
}

func (a *App) overviewPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Text(c, a.t("overview.title", "总览")).FontSize(22).Bold()
	a.debugPortBanner(c)

	if a.statusNotice != "" {
		mygoui.Text(c, a.statusNotice).FontSize(12).TextColor(t.Accent)
	}

	// Account & Quota Card
	a.card(c, "", func() {
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			display := "未检测到已登录账号"
			if a.liveQuota.Connected {
				display = a.liveQuota.Email
				if a.liveQuota.Name != "" && a.liveQuota.Name != a.liveQuota.Email {
					display = a.liveQuota.Name
				}
			}
			mygoui.Text(c, display).FontSize(16).Bold()
			if a.liveQuota.Connected && a.liveQuota.Tier != "" {
				mygoui.Badge(c, a.liveQuota.Tier)
			}
			mygoui.Spacer(c)
			if mygoui.Button(c, "刷新配额").Clicked() {
				a.refreshAsync()
			}
		})
		if a.liveQuota.Connected && a.liveQuota.Email != "" && a.liveQuota.Name != "" && a.liveQuota.Name != a.liveQuota.Email {
			mygoui.Text(c, a.liveQuota.Email).FontSize(12).TextColor(t.TextMuted)
		}

		if !a.liveQuota.Connected {
			msg := a.liveQuota.Error
			if msg == "" {
				msg = "启动 Antigravity 并登录后将自动显示 5 小时与每周配额水位"
			}
			mygoui.Text(c, msg).FontSize(12).TextColor(t.TextMuted)
		} else if len(a.liveQuota.Windows) == 0 {
			mygoui.Text(c, "已连接账号，暂未上报模型配额窗口").FontSize(12).TextColor(t.TextMuted)
		} else {
			mygoui.Grid(c).Columns(2).GapX(20).GapY(12).Children(func() {
				for _, w := range a.liveQuota.Windows {
					a.quotaBarRow(c, w)
				}
			})
		}
	})

	// 3-Column Bento KPI Cards
	mygoui.Grid(c).Columns(3).Gap(12).Children(func() {
		a.metricCard(c, "今日 Token 用量", usage.FormatTokens(a.usageSummary.TodayTokens),
			fmt.Sprintf("累计 %s Token", usage.FormatTokens(a.usageSummary.TotalTokens)))
		a.metricCard(c, "今日费用估算", usage.FormatUSD(a.usageSummary.TodayUSD),
			fmt.Sprintf("累计 %s", usage.FormatUSD(a.usageSummary.TotalUSD)))
		a.metricCard(c, "今日请求数", fmt.Sprintf("%d", a.usageSummary.TodayRequests),
			fmt.Sprintf("累计 %d 次", a.usageSummary.TotalRequests))
	})

	// 2-Column Bottom Bento
	mygoui.Grid(c).Columns(2).Gap(12).Children(func() {
		a.card(c, "", func() {
			peakHour, peakVal := 0, int64(0)
			for h, v := range a.usageSummary.TodayHourly {
				if v > peakVal {
					peakHour, peakVal = h, v
				}
			}
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "今日时段消耗").FontSize(14).Bold().Grow(1)
				badge := "24 小时分布"
				if peakVal > 0 {
					badge = fmt.Sprintf("峰值 %02d时 · %s", peakHour, usage.FormatTokens(peakVal))
				}
				mygoui.Badge(c, badge)
			})
			hourlyBuckets := make([]usage.ChartBucket, 24)
			for h := 0; h < 24; h++ {
				hourlyBuckets[h] = usage.ChartBucket{Label: fmt.Sprintf("%02d时", h), Tokens: a.usageSummary.TodayHourly[h]}
			}
			a.barChart(c, hourlyBuckets, 150)
		})

		a.card(c, "", func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "今日活跃模型").FontSize(14).Bold().Grow(1)
				cnt := "暂无调用"
				if len(a.usageSummary.TodayModels) > 0 {
					cnt = fmt.Sprintf("%d", len(a.usageSummary.TodayModels))
				}
				mygoui.Badge(c, cnt)
			})
			if len(a.usageSummary.TodayModels) == 0 {
				mygoui.Column(c).Padding(28, 0).Center().Children(func() {
					mygoui.Text(c, "当前时间范围暂无模型调用记录").FontSize(12).TextColor(t.TextMuted)
				})
				return
			}
			total := max(int64(1), a.usageSummary.TodayTokens)
			limit := min(3, len(a.usageSummary.TodayModels))
			for i := 0; i < limit; i++ {
				m := a.usageSummary.TodayModels[i]
				pct := float64(m.TotalTokens) * 100 / float64(total)
				mygoui.Column(c.Key("topm-"+m.Model)).Gap(4).Padding(6, 0).Children(func() {
					mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
						mygoui.Text(c, m.Model).FontSize(13).Bold().Grow(1)
						mygoui.Text(c, fmt.Sprintf("%s Token", usage.FormatTokens(m.TotalTokens))).FontSize(12).Bold()
					})
					mygoui.Meter(c, pct, 0, 100, &mygoui.MeterLevels{
						NormalColor: mygoui.RGB(37, 99, 235),
					}).Label(m.Model)
					mygoui.Text(c, fmt.Sprintf("%d req · %.0f%% · %s", m.RequestCount, pct, usage.FormatUSD(m.EstimatedUSD))).
						FontSize(11).TextColor(t.TextMuted)
				})
			}
		})
	})
}

func (a *App) accountsPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
		mygoui.Text(c, a.t("nav.accounts", "账号")).FontSize(22).Bold().Grow(1)
		collapseLabel := "收起配额"
		if a.allCollapsed {
			collapseLabel = "展开配额"
		}
		if mygoui.Button(c, collapseLabel).Clicked() {
			a.allCollapsed = !a.allCollapsed
			for _, acc := range a.savedAccounts {
				a.collapsedAccounts[acc.Email] = a.allCollapsed
			}
			if a.liveQuota.Email != "" {
				a.collapsedAccounts[a.liveQuota.Email] = a.allCollapsed
			}
		}
		if mygoui.Button(c, "刷新配额").Clicked() {
			a.refreshAsync()
		}
		if mygoui.PrimaryButton(c, "添加账号…").Clicked() {
			a.showAddAccountModal = true
		}
	})

	a.debugPortBanner(c)

	// Smart Switch Card
	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgSwitch, mygoui.RGBA(37, 99, 235, 0.12), mygoui.RGB(37, 99, 235))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
					mygoui.Text(c, "智能切换").FontSize(14).Bold()
					stateText := "已停用"
					if a.autoSwitch.Enabled {
						stateText = "已启用"
					}
					mygoui.Badge(c, stateText)
				})
				mygoui.Text(c, fmt.Sprintf("剩余 ≤ %.0f%% 时推荐候选 (≥ %.0f%%)", a.autoSwitch.ThresholdPct, a.autoSwitch.TargetPct)).
					FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Button(c, "配置…").Clicked() {
				a.autoSwitchThreshText = fmt.Sprintf("%.0f", a.autoSwitch.ThresholdPct)
				a.autoSwitchTargetText = fmt.Sprintf("%.0f", a.autoSwitch.TargetPct)
				a.showAutoSwitchModal = true
			}
			if mygoui.Switch(c, &a.autoSwitch.Enabled).Label("智能切换").Changed() {
				_ = accounts.SaveAutoSwitch(a.autoSwitch)
			}
		})
	})

	if a.accountNotice != "" {
		mygoui.Text(c, a.accountNotice).FontSize(12).TextColor(t.Accent)
	}

	// Render live active account if not yet saved, plus all saved accounts
	seenActive := false
	for _, acc := range a.savedAccounts {
		acc := acc
		isActive := a.liveQuota.Connected && acc.Email == a.liveQuota.Email
		if isActive {
			seenActive = true
		}
		windows := acc.Windows
		tier := acc.SubscriptionTier
		displayName := acc.Name
		if displayName == "" {
			displayName = acc.Email
		}
		updatedAt := acc.UpdatedAt
		if isActive {
			if len(a.liveQuota.Windows) > 0 {
				windows = a.liveQuota.Windows
			}
			if a.liveQuota.Tier != "" {
				tier = a.liveQuota.Tier
			}
			updatedAt = a.liveQuota.ProbedAt.Unix()
		}
		a.renderAccountCard(c, acc.Email, displayName, tier, isActive, true, windows, updatedAt)
	}
	if a.liveQuota.Connected && !seenActive {
		displayName := a.liveQuota.Name
		if displayName == "" {
			displayName = a.liveQuota.Email
		}
		a.renderAccountCard(c, a.liveQuota.Email, displayName, a.liveQuota.Tier, true, false, a.liveQuota.Windows, a.liveQuota.ProbedAt.Unix())
	}
	if !a.liveQuota.Connected && len(a.savedAccounts) == 0 {
		a.card(c, "", func() {
			mygoui.Text(c, "暂未检测到账号；请登录 Antigravity 后点击右上角「添加账号…」保存快照。").FontSize(13).TextColor(t.TextMuted)
		})
	}
}

func (a *App) renderAccountCard(c *mygoui.Context, email, displayName, tier string, isActive, isSaved bool, windows []quota.QuotaWindow, updatedAt int64) {
	t := c.Theme()
	collapsed := a.collapsedAccounts[email]

	cardBg := t.Surface
	borderCol := t.Border
	if isActive {
		borderCol = mygoui.RGBA(37, 99, 235, 0.45)
	}
	mygoui.Column(c.Key("acard-"+email)).Padding(16).Gap(12).Radius(12).Background(cardBg).Border(1, borderCol).Children(func() {
		mygoui.Column(c).Gap(6).Children(func() {
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, displayName).FontSize(15).Bold().NoWrap()
				if tier != "" {
					mygoui.Badge(c, tier)
				}
				if isActive {
					mygoui.Badge(c, "当前账号").Background(mygoui.RGBA(37, 99, 235, 0.14)).TextColor(mygoui.RGB(37, 99, 235))
				}
				mygoui.Spacer(c)
			if isActive {
				if a.svgIconButton(c, svgRefresh, "重新保存当前账号快照", a.acctBusy.Load()).Clicked() {
					a.snapshotCurrentAccountAsync()
				}
			} else if isSaved {
				if mygoui.PrimaryButton(c, "切换").Disabled(a.acctBusy.Load()).Clicked() {
					a.switchAccountAsync(email)
				}
			}
			if isSaved {
				if mygoui.Button(c, "重命名").Clicked() {
					a.renameTargetEmail = email
					a.renameInput = displayName
					a.showRenameModal = true
				}
				if mygoui.Button(c, "删除").Clicked() {
					a.deleteTargetEmail = email
					a.showDeleteModal = true
				}
			}
			toggleIcon := svgChevronUp
			toggleLabel := "收起配额"
			if collapsed {
				toggleIcon = svgChevronDown
				toggleLabel = "展开配额"
			}
			if a.svgIconButton(c, toggleIcon, toggleLabel, false).Clicked() {
				a.collapsedAccounts[email] = !collapsed
			}
			})
			if displayName != email || isActive {
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
					if displayName != email {
						mygoui.Text(c, email).FontSize(12).TextColor(t.TextMuted)
					}
					if isActive {
						mygoui.Badge(c, "切换前请刷新登录凭据; 当前运行账号可重新保存当前账号").
							Background(mygoui.RGBA(217, 119, 6, 0.14)).TextColor(mygoui.RGB(180, 83, 9))
					}
				})
			}
		})

		if !collapsed {
			if len(windows) == 0 {
				mygoui.Text(c, "暂无配额窗口数据").FontSize(12).TextColor(t.TextMuted)
			} else {
				mygoui.Grid(c).Columns(2).GapX(20).GapY(10).Children(func() {
					for _, w := range windows {
						a.quotaBarRow(c, w)
					}
				})
			}
			if updatedAt > 0 {
				mygoui.Text(c, "更新于 "+time.Unix(updatedAt, 0).Format("01-02 15:04")).FontSize(11).TextColor(t.TextMuted)
			}
		}
	})
}

func (a *App) usagePage(c *mygoui.Context) {
	t := c.Theme()

	acctOptions := []string{"全部账号"}
	for _, acc := range a.savedAccounts {
		acctOptions = append(acctOptions, acc.Email)
	}
	if a.liveQuota.Connected && a.liveQuota.Email != "" {
		found := false
		for _, o := range acctOptions {
			if o == a.liveQuota.Email {
				found = true
				break
			}
		}
		if !found {
			acctOptions = append(acctOptions, a.liveQuota.Email)
		}
	}

	mygoui.Column(c).Gap(10).Children(func() {
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, a.t("nav.usage", "用量")).FontSize(22).Bold().Grow(1)
			if mygoui.Button(c, "刷新").Clicked() {
				a.refreshAsync()
			}
			if mygoui.Button(c, "导出…").Clicked() {
				path, err := usage.ExportJSON(a.usageSummary)
				if err != nil {
					a.statusNotice = fmt.Sprintf("导出失败: %v", err)
				} else {
					a.statusNotice = "已导出用量报告至: " + path
				}
			}
		})
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			if mygoui.Select(c, &a.usageAcctFilter, acctOptions).Label("账号筛选").Changed() {
				a.refreshAsync()
			}
			mygoui.Spacer(c)
			if mygoui.Segmented(c, &a.usagePeriod, "今日", "近 7 天", "近 30 天", "全部").Label("统计周期").Changed() {
				a.refreshAsync()
			}
		})
	})

	periodLabels := []string{"今日", "近 7 天", "近 30 天", "全部"}
	periodLabel := periodLabels[min(max(0, a.usagePeriod), 3)]

	// Token Usage Card + Bar Chart
	a.card(c, "", func() {
		mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "Token 用量").FontSize(13).TextColor(t.TextMuted).Grow(1)
			mygoui.Badge(c, periodLabel)
		})
		mygoui.Text(c, usage.FormatTokens(a.usageSummary.PeriodTokens)).FontSize(30).Bold()
		mygoui.Text(c, fmt.Sprintf("输入 %s · 缓存读取 %s · 输出 %s",
			usage.FormatTokens(a.usageSummary.PeriodInput),
			usage.FormatTokens(a.usageSummary.PeriodCached),
			usage.FormatTokens(a.usageSummary.PeriodOutput))).FontSize(12).TextColor(t.TextMuted)
		a.barChart(c, a.usageSummary.Buckets, 175)
	})

	// 2-Column Middle: API Cost Donut + Requests & Cache
	mygoui.Grid(c).Columns(2).Gap(12).Children(func() {
		a.card(c, "", func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "API 费用估算").FontSize(15).Bold().Grow(1)
				mygoui.Text(c, usage.FormatUSD(a.usageSummary.PeriodUSD)).FontSize(22).Bold()
			})
			a.costDonutRow(c)
		})

		a.card(c, "", func() {
			mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "请求与缓存").FontSize(15).Bold().Grow(1)
				if mygoui.Button(c, "API 定价").Clicked() {
					a.pricingEntries = usage.LoadPricingTable()
					a.showPricingModal = true
				}
				if mygoui.Button(c, "请求明细…").Clicked() {
					a.showEventsModal = true
				}
			})
			mygoui.Divider(c)
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "请求总数").FontSize(13).Bold()
					mygoui.Text(c, fmt.Sprintf("平均每次约 %s Token", usage.FormatTokens(a.usageSummary.AvgTokensPerReq))).FontSize(12).TextColor(t.TextMuted)
				})
				mygoui.Text(c, fmt.Sprintf("%d", a.usageSummary.PeriodRequests)).FontSize(22).Bold()
			})
			mygoui.Divider(c)
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "缓存命中率").FontSize(13).Bold()
					mygoui.Text(c, fmt.Sprintf("读取 %s Token", usage.FormatTokens(a.usageSummary.PeriodCached))).FontSize(12).TextColor(t.TextMuted)
				})
				mygoui.Text(c, fmt.Sprintf("%.1f%%", a.usageSummary.CacheHitRate)).FontSize(22).Bold()
			})
			mygoui.Meter(c, a.usageSummary.CacheHitRate, 0, 100, &mygoui.MeterLevels{
				NormalColor: mygoui.RGB(37, 99, 235),
			}).Label("缓存命中率")
		})
	})

	// Model Usage List Card
	a.card(c, "", func() {
		mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "模型用量").FontSize(15).Bold().Grow(1)
			mygoui.Badge(c, fmt.Sprintf("%d 个模型", len(a.usageSummary.Models)))
		})
		if len(a.usageSummary.Models) == 0 {
			mygoui.Text(c, "当前时间范围暂无模型用量记录").FontSize(12).TextColor(t.TextMuted)
			return
		}
		totalUSD := a.usageSummary.PeriodUSD
		totalTok := max(int64(1), a.usageSummary.PeriodTokens)
		for _, m := range a.usageSummary.Models {
			share := float64(m.TotalTokens) * 100 / float64(totalTok)
			if totalUSD > 0 {
				share = m.EstimatedUSD * 100 / totalUSD
			}
			iconKey := "gemini"
			low := m.Model
			if len(low) > 0 && (low[0] == 'c' || low[0] == 'C') {
				iconKey = "claude"
			} else if len(low) > 0 && (low[0] == 'g' && (len(low) > 1 && low[1] == 'p')) {
				iconKey = "openai"
			}
			mygoui.Row(c.Key("umrow-"+m.Model)).Gap(10).AlignItems(mygoui.Center).Padding(6, 0).Children(func() {
				a.modelIconBadge(c, iconKey)
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, m.Model).FontSize(13).Bold().NoWrap()
					mygoui.Text(c, fmt.Sprintf("%s Token · %d req", usage.FormatTokens(m.TotalTokens), m.RequestCount)).FontSize(11).TextColor(t.TextMuted).NoWrap()
				})
				mygoui.Column(c).AlignItems(mygoui.End).Gap(2).Children(func() {
					mygoui.Text(c, usage.FormatUSD(m.EstimatedUSD)).FontSize(13).Bold().NoWrap()
					mygoui.Text(c, fmt.Sprintf("占比 %.0f%%", share)).FontSize(11).TextColor(t.TextMuted).NoWrap()
				})
			})
		}
	})
}

func (a *App) costDonutRow(c *mygoui.Context) {
	t := c.Theme()
	palette := []mygoui.Color{
		mygoui.RGB(37, 99, 235),
		mygoui.RGB(16, 185, 129),
		mygoui.RGB(245, 158, 11),
		mygoui.RGB(139, 92, 246),
	}
	models := a.usageSummary.Models
	if len(models) == 0 || a.usageSummary.PeriodUSD <= 0 {
		mygoui.Column(c).Padding(24, 0).Center().Children(func() {
			mygoui.Text(c, "暂无费用数据").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}
	limit := min(4, len(models))
	shares := make([]float64, limit)
	for i := 0; i < limit; i++ {
		shares[i] = max(0.0001, models[i].EstimatedUSD)
	}
	slices := chart.Pie(shares, 0.03)

	mygoui.Row(c).Gap(14).AlignItems(mygoui.Center).Children(func() {
		ring := mygoui.Box(c).Size(112, 112).Label("API 费用估算")
		ring.Draw(func(p *mygoui.Painter, r mygoui.Rect) {
			cx, cy := r.X+r.W/2, r.Y+r.H/2
			for i, s := range slices {
				p.FillPath(chart.Sector(cx, cy, 34, 50, s), palette[i%len(palette)])
			}
		})
		ring.Children(func() {
			mygoui.Column(c).Fill().Center().Children(func() {
				mygoui.Text(c, usage.FormatUSD(a.usageSummary.PeriodUSD)).FontSize(14).Bold()
				mygoui.Text(c, "估算").FontSize(11).TextColor(t.TextMuted)
			})
		})

		mygoui.Column(c).Grow(1).Gap(8).Children(func() {
			total := max(0.0001, a.usageSummary.PeriodUSD)
			for i := 0; i < limit; i++ {
				m := models[i]
				pct := m.EstimatedUSD * 100 / total
				mygoui.Row(c.Key("donut-"+m.Model)).Gap(8).AlignItems(mygoui.Center).Children(func() {
					mygoui.Box(c).Size(8, 8).Radius(4).Background(palette[i%len(palette)])
					mygoui.Column(c).Gap(1).Children(func() {
						mygoui.Text(c, m.Model).FontSize(12).Bold()
						mygoui.Text(c, fmt.Sprintf("%s · %.0f%%", usage.FormatUSD(m.EstimatedUSD), pct)).FontSize(11).TextColor(t.TextMuted)
					})
				})
			}
		})
	})
}

func (a *App) barChart(c *mygoui.Context, buckets []usage.ChartBucket, height float32) {
	t := c.Theme()
	if len(buckets) == 0 {
		buckets = make([]usage.ChartBucket, 24)
		for i := 0; i < 24; i++ {
			buckets[i] = usage.ChartBucket{Label: fmt.Sprintf("%02d时", i)}
		}
	}
	labels := make([]string, len(buckets))
	vals := make([]float64, len(buckets))
	for i, b := range buckets {
		labels[i] = b.Label
		vals[i] = float64(b.Tokens)
	}
	x := chart.NewBand(labels...).Padding(0.35, 0.15)
	_, hi := chart.Extent(vals...)
	if hi <= 0 {
		hi = 1000
	}
	y := chart.NewLinear(0, hi).Nice(3)
	yTicks := y.Ticks(3)
	for i := range yTicks {
		yTicks[i].Label = usage.FormatTokens(int64(yTicks[i].Value))
	}
	var xTicks []chart.Tick
	step := max(1, len(labels)/5)
	for i := 0; i < len(labels); i += step {
		xTicks = append(xTicks, chart.Tick{At: x.At(i), Label: labels[i]})
	}
	if len(labels) > 1 && (len(labels)-1)%step != 0 {
		xTicks = append(xTicks, chart.Tick{At: x.At(len(labels) - 1), Label: labels[len(labels)-1]})
	}

	chart.Plot(c, chart.Axes{Left: yTicks, Bottom: xTicks}, func(area mygoui.Element) {
		area.Draw(func(p *mygoui.Painter, r mygoui.Rect) {
			for _, tk := range yTicks {
				p.Line(r.X, chart.Y(r, tk.At), r.X+r.W, chart.Y(r, tk.At), 1, t.Border)
			}
		})
		for i, v := range vals {
			if v <= 0 {
				continue
			}
			chart.Rect(c, x.Band(i), y.Span(0, v)).Radius(3).Background(t.Accent).
				Tooltip(fmt.Sprintf("%s: %s Token", labels[i], usage.FormatTokens(buckets[i].Tokens)))
		}
	}).Label("Token 分布").Height(height).Gap(6)
}

func (a *App) localizationPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Column(c).Gap(4).Children(func() {
		mygoui.Text(c, "Antigravity 深度汉化").FontSize(22).Bold()
		mygoui.Text(c, "由你的自有汉化引擎与白名词库全权接管，自动避让 GravityBox 重复注入").FontSize(12).TextColor(t.TextMuted)
	})

	if a.statusNotice != "" {
		mygoui.Text(c, a.statusNotice).FontSize(12).TextColor(t.Accent)
	}

	mygoui.Text(c, "当前汉化与防冲突状态").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		a.kvRow(c, "客户端版本", "Antigravity v"+orDash(a.asarStatus.AppVersion))
		mygoui.Divider(c)
		mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "ASAR 补丁状态").FontSize(13).Bold().Grow(1)
			if a.asarStatus.IsPatched {
				mygoui.Badge(c, "已启用深度汉化").Background(mygoui.RGBA(22, 163, 74, 0.14)).TextColor(mygoui.RGB(22, 163, 74))
			} else {
				mygoui.Badge(c, "未启用")
			}
		})
		mygoui.Divider(c)
		bakMsg := "未生成备份"
		if a.asarStatus.HasBackup {
			bakMsg = "已安全备份 (app.asar.bak)"
		}
		a.kvRow(c, "官方原版备份", bakMsg)
		mygoui.Divider(c)
		conflictMsg := "运行时隔离世界注入模式"
		if a.asarStatus.IsPatched {
			conflictMsg = "已自动接管 · GravityBox 仅运行 HUD 增强，不重复改写界面"
		}
		mygoui.Column(c).Gap(2).Children(func() {
			mygoui.Text(c, "重复冲突防护").FontSize(13).Bold()
			mygoui.Text(c, conflictMsg).FontSize(12).TextColor(t.TextMuted)
		})
		mygoui.Row(c).Gap(8).Justify(mygoui.End).Children(func() {
			if mygoui.Button(c, "恢复官方原版").Disabled(!a.asarStatus.HasBackup || a.asarBusy.Load()).Clicked() {
				a.restoreAsarAsync()
			}
			if mygoui.PrimaryButton(c, "一键应用深度汉化并热生效").Disabled(!a.asarStatus.AppFound || a.asarBusy.Load()).Clicked() {
				a.applyDeepAsarAsync()
			}
		})
	})

	mygoui.Text(c, "云端词库同步（途径 A 上游）").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			mygoui.Column(c).Grow(1).Gap(3).Children(func() {
				mygoui.Text(c, fmt.Sprintf("云端同步版本 · v%s (%d 条)", orDash(a.zhPackInfo.AppVersion), a.zhPackInfo.EntriesCount)).FontSize(14).Bold()
				mygoui.Text(c, fmt.Sprintf("更新时间：%s · 源：%s", orDash(a.zhPackInfo.UpdatedAt), a.zhPackInfo.Source)).FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Button(c, "从 GitHub 同步最新汉化包").Disabled(a.syncBusy.Load()).Clicked() {
				a.syncZhPackAsync()
			}
		})
	})

	mygoui.Text(c, "新版本文本排查（途径 A 采集）").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			mygoui.Column(c).Grow(1).Gap(3).Children(func() {
				mygoui.Text(c, "自动差分扫描新版本未译英文").FontSize(14).Bold()
				mygoui.Text(c, "Antigravity 自动更新后，可一键扫描前端 main.js、app.asar 与内置插件中尚未覆盖的英文 UI 文本。").FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Button(c, "扫描当前版本新增文本").Disabled(!a.asarStatus.AppFound || a.syncBusy.Load()).Clicked() {
				a.scanNewStringsAsync()
			}
		})
	})
}

func (a *App) settingsPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Text(c, a.t("settings.title", "设置")).FontSize(22).Bold()

	if a.statusNotice != "" {
		mygoui.Text(c, a.statusNotice).FontSize(12).TextColor(t.Accent)
	}

	mygoui.Text(c, "Antigravity 增强").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgGlobe, mygoui.RGBA(37, 99, 235, 0.12), mygoui.RGB(37, 99, 235))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "显示语言").FontSize(14).Bold()
				sub := "同时决定 GravityBox 与 Antigravity 的界面语言"
				if a.asarStatus.IsPatched {
					sub = "已自动接管 · GravityBox 仅运行 HUD 增强，不重复改写界面"
				}
				mygoui.Text(c, sub).FontSize(12).TextColor(t.TextMuted)
			})
			if a.asarStatus.IsPatched {
				mygoui.Badge(c, "深度汉化")
			}
			options := []string{"跟随系统", "简体中文", "English"}
			if mygoui.Select(c, &a.selectedLang, options).Label("显示语言").Changed() {
				switch a.selectedLang {
				case "English":
					a.cfg.AppLanguage = "en"
				case "简体中文":
					a.cfg.AppLanguage = "zh-Hans"
				default:
					a.cfg.AppLanguage = "system"
				}
				a.saveConfig()
			}
		})
		mygoui.Divider(c)
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgHud, mygoui.RGBA(139, 92, 246, 0.12), mygoui.RGB(139, 92, 246))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "对话状态栏").FontSize(14).Bold()
				mygoui.Text(c, "在 Antigravity 输入框工具栏显示实时速度、上下文、缓存命中与四态指示").FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Switch(c, &a.cfg.HudEnabled).Label("对话状态栏").Changed() {
				a.saveConfig()
			}
		})
		if a.cfg.HudEnabled {
			mygoui.Divider(c)
			mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "状态栏布局").FontSize(13).Bold()
					mygoui.Text(c, "紧凑模式在宽屏下嵌入中段工具栏，详细模式独立显示 TTFT 与 Out").FontSize(12).TextColor(t.TextMuted)
				})
				layouts := []string{"紧凑 (默认)", "详细 (含 TTFT/Out)"}
				if mygoui.Select(c, &a.selectedHudLayout, layouts).Label("状态栏布局").Changed() {
					a.cfg.TelemetryLayout = hudLayoutFromLabel(a.selectedHudLayout)
					a.saveConfig()
				}
			})
			mygoui.Divider(c)
			mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "缓存命中口径").FontSize(13).Bold()
					mygoui.Text(c, "控制紧凑状态栏 Cache % 显示会话累计或当前单步命中率").FontSize(12).TextColor(t.TextMuted)
				})
				modes := []string{"会话累计命中率", "当前步命中率"}
				if mygoui.Select(c, &a.selectedCacheMode, modes).Label("缓存命中口径").Changed() {
					a.cfg.TelemetryCacheMode = cacheModeFromLabel(a.selectedCacheMode)
					a.saveConfig()
				}
			})
			mygoui.Divider(c)
			mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "上下文窗口上限").FontSize(13).Bold()
					mygoui.Text(c, "默认从会话数据库自动读取模型真实上下文容量").FontSize(12).TextColor(t.TextMuted)
				})
				limits := []string{"自动识别", "256k", "512k", "1M", "2M"}
				if mygoui.Select(c, &a.selectedCtxLimit, limits).Label("上下文窗口上限").Changed() {
					a.cfg.ManualContextLimit = ctxLimitFromLabel(a.selectedCtxLimit)
					a.saveConfig()
				}
			})
		}
	})

	mygoui.Text(c, "主题外观").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgPalette, mygoui.RGBA(43, 127, 255, 0.12), mygoui.RGB(43, 127, 255))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "GravityBox 界面外观").FontSize(14).Bold()
				mygoui.Text(c, "切换 GravityBox 主窗口与托盘面板的浅色/深色外观").FontSize(12).TextColor(t.TextMuted)
			})
			appThemes := []string{"跟随系统", "浅色", "深色"}
			if mygoui.Select(c, &a.selectedAppTheme, appThemes).Label("GravityBox 界面外观").Changed() {
				a.cfg.AppTheme = appThemeFromLabel(a.selectedAppTheme)
				a.saveConfig()
			}
		})
		mygoui.Divider(c)
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgPalette, mygoui.RGBA(16, 185, 129, 0.12), mygoui.RGB(5, 150, 105))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "Antigravity Codex 昼夜主题").FontSize(14).Bold()
				mygoui.Text(c, "注入 Codex 纯白浅色 / 炭灰深色配色、双层圆角输入框与界面排版优化").FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Switch(c, &a.cfg.CodexThemeEnabled).Label("Antigravity Codex 昼夜主题").Changed() {
				a.saveConfig()
			}
		})
		if a.cfg.CodexThemeEnabled {
			mygoui.Divider(c)
			mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "Codex 主题模式").FontSize(13).Bold()
					mygoui.Text(c, "默认随 Antigravity 明暗自动切换，也可固定纯白浅色或炭灰深色").FontSize(12).TextColor(t.TextMuted)
				})
				presets := []string{"自动跟随 IDE", "Codex 纯白浅色", "Codex 炭灰深色"}
				if mygoui.Select(c, &a.selectedCodexPreset, presets).Label("Codex 主题模式").Changed() {
					a.cfg.CodexThemePreset = codexPresetFromLabel(a.selectedCodexPreset)
					a.saveConfig()
				}
			})
			mygoui.Divider(c)
			mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "主题强调色").FontSize(13).Bold()
					mygoui.Text(c, "同步应用于 GravityBox 与 Antigravity 主按钮及选区高亮").FontSize(12).TextColor(t.TextMuted)
				})
				accents := []string{"经典蓝", "翡翠绿", "靛青紫", "琥珀金"}
				if mygoui.Select(c, &a.selectedAccent, accents).Label("主题强调色").Changed() {
					a.cfg.AccentColor = accentFromLabel(a.selectedAccent)
					a.saveConfig()
				}
			})
			mygoui.Divider(c)
			mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
				mygoui.Column(c).Grow(1).Gap(2).Children(func() {
					mygoui.Text(c, "对话正文字号").FontSize(13).Bold()
					mygoui.Text(c, "覆盖 Antigravity 对话消息正文显示字号").FontSize(12).TextColor(t.TextMuted)
				})
				fonts := []string{"默认", "14px", "15px", "16px"}
				if mygoui.Select(c, &a.selectedChatFont, fonts).Label("对话正文字号").Changed() {
					a.cfg.ChatFontSize = chatFontFromLabel(a.selectedChatFont)
					a.saveConfig()
				}
			})
		}
	})

	mygoui.Text(c, "配额与计价").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgRefresh, mygoui.RGBA(37, 99, 235, 0.12), mygoui.RGB(37, 99, 235))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "配额刷新间隔").FontSize(14).Bold()
				mygoui.Text(c, "定时更新已保存账号的剩余配额").FontSize(12).TextColor(t.TextMuted)
			})
			intervals := []string{"手动刷新", "每 1 分钟", "每 5 分钟", "每 15 分钟", "每 30 分钟"}
			if mygoui.Select(c, &a.selectedInterval, intervals).Label("配额刷新间隔").Changed() {
				a.cfg.QuotaRefreshInterval = intervalFromLabel(a.selectedInterval)
				a.saveConfig()
			}
		})
		mygoui.Divider(c)
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgDollar, mygoui.RGBA(37, 99, 235, 0.12), mygoui.RGB(37, 99, 235))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "自动更新价格基准").FontSize(14).Bold()
				mygoui.Text(c, "自动同步公开模型费率，每 24 小时检查一次").FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Button(c, "立即检查").Clicked() {
				go a.checkPricingAsync()
			}
			if mygoui.Switch(c, &a.cfg.AutoPricingSync).Label("自动更新价格基准").Changed() {
				a.saveConfig()
			}
		})
	})

	mygoui.Text(c, "系统与存储").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgBolt, mygoui.RGBA(245, 158, 11, 0.12), mygoui.RGB(217, 119, 6))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "登录时启动").FontSize(14).Bold()
				mygoui.Text(c, "登录 Windows 时自动在系统托盘驻留").FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Switch(c, &a.cfg.LaunchAtLogin).Label("登录时启动").Changed() {
				if err := win32.SetLaunchAtLogin(a.cfg.LaunchAtLogin); err != nil {
					a.statusNotice = fmt.Sprintf("设置开机启动失败: %v", err)
				}
				a.saveConfig()
			}
		})
		mygoui.Divider(c)
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgLink, mygoui.RGBA(107, 114, 128, 0.12), t.TextMuted)
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "跟随 Antigravity 启停").FontSize(14).Bold()
				mygoui.Text(c, "随 Antigravity 自动启动并同步退出").FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Switch(c, &a.cfg.FollowAntigravity).Label("跟随 Antigravity 启停").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Divider(c)
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			a.iconBadge(c, svgStorage, mygoui.RGBA(139, 92, 246, 0.12), mygoui.RGB(139, 92, 246))
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "存储与维护").FontSize(14).Bold()
				mygoui.Text(c, "一键释放运行缓存与超期日志，登录态与对话完好保留").FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Button(c, "管理存储…").Clicked() {
				a.showStorageModal = true
			}
		})
	})
}

func (a *App) aboutPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Text(c, a.t("nav.about", "关于")).FontSize(22).Bold()

	a.card(c, "", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			if a.logoBmp != nil {
				mygoui.Image(c, a.logoBmp).Size(44, 44).Fit(mygoui.Contain).Radius(10)
			}
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
					mygoui.Text(c, "GravityBox").FontSize(16).Bold()
					mygoui.Badge(c, fmt.Sprintf("v%s (Windows)", appVersion)).
						Background(mygoui.RGBA(37, 99, 235, 0.12)).TextColor(mygoui.RGB(37, 99, 235))
				})
				mygoui.Text(c, a.updateNotice).FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Button(c, "更新日志 ↗").Clicked() {
				_ = win32.OpenExternalURL("https://github.com/KAIKAIKAISER/gravitybox-mygo/releases")
			}
			if mygoui.Button(c, "检查更新").Disabled(a.updBusy.Load()).Clicked() {
				go a.checkUpdatesAsync()
			}
		})
		mygoui.Divider(c)
		mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "自动检查更新").FontSize(14).Bold().Grow(1)
			if mygoui.Switch(c, &a.cfg.AutoCheckUpdates).Label("自动检查更新").Changed() {
				a.saveConfig()
			}
		})
	})

	mygoui.Text(c, "运行信息").FontSize(12).TextColor(t.TextMuted)
	a.card(c, "", func() {
		targetVer := "未检测到"
		if a.asarStatus.AppVersion != "" {
			targetVer = "Antigravity " + a.asarStatus.AppVersion
		}
		a.kvRow(c, "目标应用", targetVer)
		mygoui.Divider(c)
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "GravityBox 数据目录").FontSize(13).Bold()
				mygoui.Text(c, config.StateDir()).FontSize(12).TextColor(t.TextMuted)
			})
			if mygoui.Button(c, "复制目录路径").Clicked() {
				mygo.Clipboard.WriteText(config.StateDir())
				a.statusNotice = "已复制数据目录路径"
			}
			if mygoui.Button(c, "在资源管理器中打开").Clicked() {
				_ = win32.OpenInExplorer(config.StateDir())
			}
		})
		mygoui.Divider(c)
		mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "GitHub Releases").FontSize(13).Bold().Grow(1)
			if mygoui.Button(c, "GitHub ↗").Clicked() {
				_ = win32.OpenExternalURL("https://github.com/KAIKAIKAISER/gravitybox-mygo/releases")
			}
		})
	})
}

func (a *App) quotaBarRow(c *mygoui.Context, w quota.QuotaWindow) {
	t := c.Theme()
	mygoui.Column(c.Key("qw-"+w.Label)).Gap(5).Children(func() {
		mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
			a.modelVectorIcon(c, w.IconKey, 14)
			mygoui.Text(c, w.Label).FontSize(12).Bold().NoWrap().Grow(1)
			if w.ResetCountdown != "" {
				mygoui.Text(c, w.ResetCountdown).FontSize(11).TextColor(t.TextMuted).NoWrap()
			}
			pctText := fmt.Sprintf("%.0f%%", w.RemainingPct)
			txt := mygoui.Text(c, pctText).FontSize(12).Bold().NoWrap()
			if w.RemainingPct <= 10 {
				txt.TextColor(mygoui.RGB(220, 38, 38))
			} else if w.RemainingPct <= 25 {
				txt.TextColor(mygoui.RGB(217, 119, 6))
			}
		})
		mygoui.Meter(c, w.RemainingPct, 0, 100, &mygoui.MeterLevels{
			Warning:       25,
			Critical:      10,
			NormalColor:   mygoui.RGB(37, 99, 235),
			WarningColor:  mygoui.RGB(217, 119, 6),
			CriticalColor: mygoui.RGB(220, 38, 38),
		}).Label(w.Label)
	})
}

func (a *App) modelVectorIcon(c *mygoui.Context, iconKey string, sz float32) {
	switch iconKey {
	case "claude":
		mygoui.Icon(c, svgClaude).FontSize(sz).TextColor(mygoui.RGB(217, 119, 6))
	case "openai":
		mygoui.Icon(c, svgOpenAI).FontSize(sz).TextColor(mygoui.RGB(16, 163, 127))
	default:
		mygoui.Icon(c, svgGemini).FontSize(sz).TextColor(mygoui.RGB(59, 130, 246))
	}
}

func (a *App) modelIconBadge(c *mygoui.Context, iconKey string) {
	bg := mygoui.RGBA(59, 130, 246, 0.12)
	fg := mygoui.RGB(37, 99, 235)
	svg := svgGemini
	switch iconKey {
	case "claude":
		bg = mygoui.RGBA(217, 119, 6, 0.12)
		fg = mygoui.RGB(217, 119, 6)
		svg = svgClaude
	case "openai":
		bg = mygoui.RGBA(16, 163, 127, 0.12)
		fg = mygoui.RGB(16, 163, 127)
		svg = svgOpenAI
	}
	a.iconBadge(c, svg, bg, fg)
}

func (a *App) svgIconButton(c *mygoui.Context, svg *mygoui.SVG, label string, disabled bool) mygoui.Element {
	t := c.Theme()
	btn := mygoui.ButtonBase(c).Padding(6, 8).Radius(8).
		Background(t.Surface).Border(1, t.Border).
		Disabled(disabled).Label(label).Tooltip(label)
	btn.Children(func() {
		mygoui.Icon(c, svg).FontSize(13).TextColor(t.Text)
	})
	return btn
}

func (a *App) pillBadge(c *mygoui.Context, text string, bg, fg mygoui.Color) {
	mygoui.Row(c).Padding(2, 8).Radius(999).Background(bg).AlignItems(mygoui.Center).Children(func() {
		mygoui.Text(c, text).FontSize(11).Bold().TextColor(fg).NoWrap()
	})
}

func (a *App) metricCard(c *mygoui.Context, title, value, sub string) {
	t := c.Theme()
	a.card(c, "", func() {
		mygoui.Text(c, title).FontSize(12).TextColor(t.TextMuted).NoWrap()
		mygoui.Text(c, value).FontSize(26).Bold().NoWrap()
		mygoui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).NoWrap()
	})
}

func (a *App) iconBadge(c *mygoui.Context, svg *mygoui.SVG, bg, fg mygoui.Color) {
	mygoui.Box(c).Size(32, 32).Radius(8).Background(bg).Center().Children(func() {
		mygoui.Icon(c, svg).FontSize(16).TextColor(fg)
	})
}

func (a *App) card(c *mygoui.Context, title string, body func()) {
	t := c.Theme()
	cardBg := mygoui.RGB(255, 255, 255)
	borderCol := mygoui.RGBA(15, 23, 42, 0.08)
	shadowCol := mygoui.RGBA(15, 23, 42, 0.05)
	if t.Dark {
		cardBg = mygoui.RGB(35, 35, 35)
		borderCol = mygoui.RGBA(255, 255, 255, 0.08)
		shadowCol = mygoui.RGBA(0, 0, 0, 0.25)
	}
	mygoui.Column(c).Padding(16).Gap(12).Radius(12).
		Background(cardBg).Border(1, borderCol).Shadow(0, 1, 3, 0, shadowCol).Children(func() {
		if title != "" {
			mygoui.Text(c, title).FontSize(14).Bold()
		}
		body()
	})
}

func (a *App) kvRow(c *mygoui.Context, label, value string) {
	t := c.Theme()
	mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
		mygoui.Text(c, label).FontSize(13).Bold().Grow(1)
		mygoui.Text(c, value).FontSize(13).TextColor(t.TextMuted)
	})
}
