package ui

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"gravitybox-mygo/internal/accounts"
	"gravitybox-mygo/internal/asar"
	"gravitybox-mygo/internal/cdp"
	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/i18n"
	"gravitybox-mygo/internal/maintenance"
	"gravitybox-mygo/internal/quota"
	"gravitybox-mygo/internal/win32"
	"gravitybox-mygo/resources"
)

// App holds all state for the GravityBox Native UI windows, tray, and background services.
type App struct {
	mainWin    *mygo.Window
	popoverWin *mygo.Window
	tray       *mygo.Tray
	quitting   bool

	cfg          config.Config
	activeTab    int
	selectedLang string

	controller *cdp.Controller
	refreshing atomic.Bool
	asarBusy   atomic.Bool
	acctBusy   atomic.Bool

	// Live probe state
	antigravityRunning bool
	appDir             string
	asarStatus         asar.Status
	cdpStatus          cdp.ControllerStatus
	liveQuota          quota.LiveStatus
	currentAccountID   string
	savedAccounts      []accounts.SavedAccount
	lastProbeAt        time.Time
	statusNotice       string
	accountNotice      string

	// Maintenance state
	cacheItems      []maintenance.CacheItem
	cacheTotalBytes int64
	lastCleanMsg    string
}

// Run initializes and runs the GravityBox MyGo Native UI application.
func Run() error {
	cfg := config.Load()
	ctrl, err := cdp.NewController(cfg.DebugPort, i18n.ResolveLocale(cfg.AppLanguage), cfg.TranslationEnabled, cfg.HudEnabled)
	if err != nil {
		return err
	}
	app := &App{
		cfg:          cfg,
		selectedLang: langLabel(cfg.AppLanguage),
		controller:   ctrl,
	}

	mygo.App.SetName("GravityBox")
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		app.quitting = true
		if app.controller != nil {
			app.controller.Stop()
		}
	})
	mygo.App.WhenReady(app.onReady)
	return mygo.App.Run()
}

func (a *App) t(key, fallback string) string {
	return i18n.T(a.cfg.AppLanguage, key, fallback)
}

func (a *App) onReady() {
	iconBytes, _ := resources.ReadFile("icon.png")

	var err error
	a.tray, err = mygo.NewTray(mygo.TrayOptions{
		Icon:    iconBytes,
		ToolTip: "GravityBox — Antigravity Companion",
	})
	if err != nil {
		log.Fatal("tray init failed: ", err)
	}

	a.mainWin = mygo.NewWindow(mygo.WindowOptions{
		Title:     "GravityBox",
		Width:     940,
		Height:    660,
		MinWidth:  780,
		MinHeight: 540,
		Hidden:    a.cfg.StartInTray,
		Content:   mygoui.View(a.mainView),
	})
	a.mainWin.OnClose(func(e *mygo.CloseEvent) {
		if !a.quitting {
			e.PreventDefault()
			a.mainWin.Hide()
		}
	})

	a.popoverWin = mygo.NewWindow(mygo.WindowOptions{
		Title:             "GravityBox Quick Panel",
		Width:             380,
		Height:            430,
		Hidden:            true,
		Frameless:         true,
		DisableResize:     true,
		DisableMinimize:   true,
		DisableMaximize:   true,
		DisableFullScreen: true,
		AlwaysOnTop:       true,
		SkipTaskbar:       true,
		Content:           mygoui.View(a.popoverView),
	})
	a.popoverWin.OnBlur(a.popoverWin.Hide)
	a.popoverWin.OnClose(func(e *mygo.CloseEvent) {
		if !a.quitting {
			e.PreventDefault()
			a.popoverWin.Hide()
		}
	})

	menu := mygo.NewMenu([]*mygo.MenuItem{
		{
			Label: a.t("tray.open_main", "打开主界面"),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.showMain(0) },
		},
		{
			Label: a.t("sidebar.open_antigravity", "打开 Antigravity"),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.launchAntigravity() },
		},
		mygo.Separator(),
		{Role: mygo.RoleQuit},
	})

	if runtime.GOOS == "linux" {
		a.tray.SetMenu(menu)
	} else {
		a.tray.OnClick(a.togglePopover)
		a.tray.OnRightClick(func() {
			a.popoverWin.Hide()
			a.tray.SetMenu(menu)
			defer a.tray.SetMenu(nil)
			a.tray.PopUpMenu()
		})
	}
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if a.tray != nil {
			a.tray.Destroy()
		}
	})

	a.refreshAsync()
	go a.pollLoop()
}

func (a *App) pollLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if a.quitting {
			return
		}
		a.refreshAsync()
	}
}

func (a *App) refreshAsync() {
	if !a.refreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.refreshing.Store(false)

		appDir := config.DetectAntigravityInstallDir(a.cfg.AppPath)
		running := win32.IsAntigravityRunning()
		asarSt := asar.InspectApp(appDir)

		a.controller.UpdateConfig(
			a.cfg.DebugPort,
			i18n.ResolveLocale(a.cfg.AppLanguage),
			a.cfg.TranslationEnabled,
			a.cfg.HudEnabled,
			asarSt.IsPatched,
		)
		cdpCtx, cdpCancel := context.WithTimeout(context.Background(), 4*time.Second)
		cdpSt := a.controller.SyncOnce(cdpCtx, running)
		cdpCancel()

		var liveQ quota.LiveStatus
		if running {
			qCtx, qCancel := context.WithTimeout(context.Background(), 4*time.Second)
			liveQ = quota.ProbeLive(qCtx)
			qCancel()
		}

		curID, savedList, _ := accounts.ListSaved()
		items, total := maintenance.ScanSafeCaches()

		mygo.RunOnMain(func() {
			a.appDir = appDir
			a.antigravityRunning = running
			a.asarStatus = asarSt
			a.cdpStatus = cdpSt
			a.liveQuota = liveQ
			a.currentAccountID = curID
			a.savedAccounts = savedList
			a.cacheItems = items
			a.cacheTotalBytes = total
			a.lastProbeAt = time.Now()
			a.invalidateVisible()
		})
	}()
}

func (a *App) invalidateVisible() {
	if a.mainWin != nil && a.mainWin.IsVisible() {
		a.mainWin.Invalidate()
	}
	if a.popoverWin != nil && a.popoverWin.IsVisible() {
		a.popoverWin.Invalidate()
	}
}

func (a *App) showMain(tab int) {
	a.activeTab = tab
	if a.popoverWin != nil {
		a.popoverWin.Hide()
	}
	if a.mainWin != nil {
		a.mainWin.Show()
		a.mainWin.Invalidate()
	}
}

func (a *App) togglePopover() {
	if a.popoverWin == nil {
		return
	}
	if a.popoverWin.IsVisible() {
		a.popoverWin.Hide()
		return
	}
	anchor := a.tray.Bounds()
	if anchor.Width == 0 || anchor.Height == 0 {
		a.popoverWin.Center()
	} else {
		area := mygo.Screen.DisplayNearestPoint(mygo.Point{
			X: anchor.X + anchor.Width/2,
			Y: anchor.Y + anchor.Height/2,
		}).WorkArea
		b := a.popoverWin.Bounds()
		x := anchor.X + (anchor.Width-b.Width)/2
		y := anchor.Y + anchor.Height + 6
		if y+b.Height > area.Y+area.Height {
			y = anchor.Y - b.Height - 6
		}
		x = max(area.X, min(x, area.X+max(0, area.Width-b.Width)))
		y = max(area.Y, min(y, area.Y+max(0, area.Height-b.Height)))
		a.popoverWin.SetPosition(x, y)
	}
	a.popoverWin.Show()
}

func (a *App) launchAntigravity() {
	dir := config.DetectAntigravityInstallDir(a.cfg.AppPath)
	if dir == "" {
		a.statusNotice = "未找到 Antigravity 安装目录"
		a.invalidateVisible()
		return
	}
	exe := filepath.Join(dir, "Antigravity.exe")
	if err := win32.LaunchAntigravity(exe, a.cfg.DebugPort); err != nil {
		a.statusNotice = fmt.Sprintf("启动失败: %v", err)
		a.invalidateVisible()
		return
	}
	a.statusNotice = fmt.Sprintf("已携带调试端口 %d 启动 Antigravity", a.cfg.DebugPort)
	a.invalidateVisible()
	a.refreshAsync()
}

func (a *App) applyDeepAsarAsync() {
	if !a.asarBusy.CompareAndSwap(false, true) {
		return
	}
	a.statusNotice = "正在写入 ASAR 深度汉化补丁并执行热注入…"
	a.invalidateVisible()
	go func() {
		defer a.asarBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		res := asar.ApplyDeepLocalization(ctx, a.appDir, config.StateDir(), a.cfg.DebugPort)
		cancel()
		mygo.RunOnMain(func() {
			a.statusNotice = res.Message
			a.invalidateVisible()
		})
		a.refreshAsync()
	}()
}

func (a *App) restoreAsarAsync() {
	if !a.asarBusy.CompareAndSwap(false, true) {
		return
	}
	a.statusNotice = "正在从 app.asar.bak 恢复官方原版…"
	a.invalidateVisible()
	go func() {
		defer a.asarBusy.Store(false)
		res := asar.RestoreBackup(a.appDir)
		mygo.RunOnMain(func() {
			a.statusNotice = res.Message
			a.invalidateVisible()
		})
		a.refreshAsync()
	}()
}

func (a *App) snapshotCurrentAccountAsync() {
	if !a.acctBusy.CompareAndSwap(false, true) {
		return
	}
	email := a.liveQuota.Email
	name := a.liveQuota.Name
	tier := a.liveQuota.Tier
	a.accountNotice = "正在保存当前登录账号凭据快照…"
	a.invalidateVisible()
	go func() {
		defer a.acctBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := accounts.SnapshotCurrentFromVscdb(ctx, email, name, tier)
		cancel()
		mygo.RunOnMain(func() {
			if err != nil {
				a.accountNotice = err.Error()
			} else {
				a.accountNotice = fmt.Sprintf("已将 %s 凭据安全存入 Windows 凭据管理器", email)
			}
			a.invalidateVisible()
		})
		a.refreshAsync()
	}()
}

func (a *App) switchAccountAsync(email string) {
	if !a.acctBusy.CompareAndSwap(false, true) {
		return
	}
	a.accountNotice = fmt.Sprintf("正在切换至 %s 并重启 Antigravity…", email)
	a.invalidateVisible()
	go func() {
		defer a.acctBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := accounts.SwitchAccount(ctx, email, a.appDir, a.cfg.DebugPort)
		cancel()
		mygo.RunOnMain(func() {
			if err != nil {
				a.accountNotice = fmt.Sprintf("切号失败: %v", err)
			} else {
				a.accountNotice = fmt.Sprintf("已切换至 %s 并拉起 Antigravity", email)
			}
			a.invalidateVisible()
		})
		a.refreshAsync()
	}()
}

func (a *App) saveConfig() {
	_ = config.Save(a.cfg)
	a.refreshAsync()
}

func (a *App) popoverView(c *mygoui.Context) {
	if c.Shortcut(0, mygoui.KeyEscape) {
		a.popoverWin.Hide()
	}
	t := c.Theme()
	mygoui.Column(c).Fill().Padding(16).Gap(10).Background(t.Background).Border(1, t.Border).Children(func() {
		mygoui.Row(c).Gap(8).Children(func() {
			mygoui.Text(c, "GravityBox").FontSize(16).Bold().Grow(1)
			mygoui.Badge(c, a.connectionBadge())
		})
		if a.liveQuota.Connected {
			mygoui.Text(c, fmt.Sprintf("%s (%s)", a.liveQuota.Email, a.liveQuota.Tier)).FontSize(12).TextColor(t.TextMuted)
			for _, f := range a.liveQuota.Families {
				mygoui.Row(c.Key("pop-"+f.Family)).Gap(8).Children(func() {
					mygoui.Text(c, f.Family).FontSize(12).Width(90)
					mygoui.Meter(c, f.RemainingPct, 0, 100, &mygoui.MeterLevels{Warning: 20, Critical: 10}).Label(f.Family).Grow(1)
					mygoui.Text(c, fmt.Sprintf("%.0f%%", f.RemainingPct)).FontSize(12).Width(38)
				})
			}
		}
		mygoui.Divider(c)
		mygoui.Row(c).Gap(10).Children(func() {
			mygoui.Text(c, "IDE 中文翻译").Grow(1)
			if mygoui.Switch(c, &a.cfg.TranslationEnabled).Label("IDE 中文翻译").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Row(c).Gap(10).Children(func() {
			mygoui.Text(c, "对话状态栏 (HUD)").Grow(1)
			if mygoui.Switch(c, &a.cfg.HudEnabled).Label("对话状态栏").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Divider(c)
		mygoui.Text(c, fmt.Sprintf("ASAR 深度汉化: %s", a.asarSummary())).FontSize(12).TextColor(t.TextMuted)
		mygoui.Text(c, fmt.Sprintf("可安全清理缓存: %s", maintenance.FormatBytes(a.cacheTotalBytes))).FontSize(12).TextColor(t.TextMuted)
		mygoui.Spacer(c)
		mygoui.Row(c).Gap(8).Children(func() {
			if mygoui.Button(c, a.t("sidebar.open_antigravity", "打开 Antigravity")).Clicked() {
				a.launchAntigravity()
			}
			mygoui.Spacer(c)
			if mygoui.PrimaryButton(c, "打开主界面 ↗").Clicked() {
				a.showMain(0)
			}
		})
	})
}

func (a *App) mainView(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Column(c).Fill().Padding(20).Gap(14).Background(t.Background).Children(func() {
		mygoui.Row(c).Gap(12).Children(func() {
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, "GravityBox").FontSize(22).Bold()
				mygoui.Text(c, "Pure MyGo Native UI · 零 Node.js · 零 WebView2").FontSize(12).TextColor(t.TextMuted)
			})
			mygoui.Badge(c, a.connectionBadge())
			if mygoui.Button(c, "刷新").Clicked() {
				a.refreshAsync()
			}
			if mygoui.PrimaryButton(c, a.t("sidebar.open_antigravity", "打开 Antigravity")).Clicked() {
				a.launchAntigravity()
			}
		})

		mygoui.Tabs(c, &a.activeTab,
			a.t("nav.overview", "总览"),
			a.t("nav.accounts", "账号与配额"),
			"缓存维护",
			a.t("nav.settings", "设置"),
			a.t("nav.about", "关于"),
		)
		mygoui.Divider(c)

		mygoui.Scroll(c).Grow(1).Gap(12).Children(func() {
			switch a.activeTab {
			case 0:
				a.overviewPage(c)
			case 1:
				a.accountsPage(c)
			case 2:
				a.maintenancePage(c)
			case 3:
				a.settingsPage(c)
			case 4:
				a.aboutPage(c)
			}
		})
	})
}

func (a *App) overviewPage(c *mygoui.Context) {
	t := c.Theme()
	a.card(c, "连接与运行状态", func() {
		a.kvRow(c, "Antigravity 安装路径", orDash(a.appDir))
		a.kvRow(c, "官方客户端版本", orDash(a.asarStatus.AppVersion))
		a.kvRow(c, "CDP 注入状态", fmt.Sprintf("%s (%d 窗口 · %d 词条)", a.cdpStatus.Mode, a.cdpStatus.Windows, a.cdpStatus.DictionaryEntries))
		a.kvRow(c, "ASAR 深度汉化", a.asarSummary())
		if a.statusNotice != "" {
			mygoui.Text(c, a.statusNotice).FontSize(12).TextColor(t.Accent)
		}
		mygoui.Row(c).Gap(8).Children(func() {
			if mygoui.PrimaryButton(c, "写入 ASAR 深度汉化").Disabled(!a.asarStatus.AppFound || a.asarBusy.Load()).Clicked() {
				a.applyDeepAsarAsync()
			}
			if mygoui.Button(c, "恢复官方原版").Disabled(!a.asarStatus.HasBackup || a.asarBusy.Load()).Clicked() {
				a.restoreAsarAsync()
			}
		})
	})

	a.card(c, "运行时热适配开关", func() {
		mygoui.Row(c).Gap(10).Children(func() {
			mygoui.Text(c, "启用 IDE 运行时中文汉化 (CDP 隔离世界)").Grow(1)
			if mygoui.Switch(c, &a.cfg.TranslationEnabled).Label("启用 IDE 运行时中文汉化").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Row(c).Gap(10).Children(func() {
			mygoui.Text(c, "启用输入框对话状态栏 (HUD)").Grow(1)
			if mygoui.Switch(c, &a.cfg.HudEnabled).Label("启用输入框对话状态栏").Changed() {
				a.saveConfig()
			}
		})
	})
}

func (a *App) accountsPage(c *mygoui.Context) {
	t := c.Theme()
	a.card(c, "当前实时账号与配额", func() {
		if !a.liveQuota.Connected {
			msg := a.liveQuota.Error
			if msg == "" {
				msg = "Antigravity 未运行或未连接本地 Language Server"
			}
			mygoui.Text(c, msg).FontSize(13).TextColor(t.TextMuted)
		} else {
			mygoui.Row(c).Gap(8).Children(func() {
				mygoui.Text(c, a.liveQuota.Email).Bold().Grow(1)
				mygoui.Badge(c, a.liveQuota.Tier)
				if mygoui.PrimaryButton(c, "保存当前账号快照").Disabled(a.acctBusy.Load()).Clicked() {
					a.snapshotCurrentAccountAsync()
				}
			})
			for _, f := range a.liveQuota.Families {
				mygoui.Row(c.Key("acct-"+f.Family)).Gap(10).Children(func() {
					mygoui.Text(c, f.Family).Width(110)
					mygoui.Meter(c, f.RemainingPct, 0, 100, &mygoui.MeterLevels{Warning: 20, Critical: 10}).Label(f.Family).Grow(1)
					mygoui.Text(c, fmt.Sprintf("%.0f%%", f.RemainingPct)).Width(48)
				})
			}
		}
		if a.accountNotice != "" {
			mygoui.Text(c, a.accountNotice).FontSize(12).TextColor(t.Accent)
		}
	})

	a.card(c, "已保存账号（安全四阶段切号）", func() {
		if len(a.savedAccounts) == 0 {
			mygoui.Text(c, "暂无已保存账号快照；请在 Antigravity 登录后点击上方「保存当前账号快照」。").FontSize(12).TextColor(t.TextMuted)
			return
		}
		for _, acc := range a.savedAccounts {
			acc := acc
			mygoui.Row(c.Key("saved-"+acc.Email)).Gap(10).Children(func() {
				mygoui.Text(c, acc.Email).Grow(1)
				if acc.SubscriptionTier != "" {
					mygoui.Badge(c, acc.SubscriptionTier)
				}
				if acc.Email == a.liveQuota.Email || acc.ID == a.currentAccountID {
					mygoui.Badge(c, "当前")
				}
				if mygoui.Button(c, "切换").Disabled(a.acctBusy.Load() || acc.Email == a.liveQuota.Email).Clicked() {
					a.switchAccountAsync(acc.Email)
				}
			})
		}
	})
}

func (a *App) maintenancePage(c *mygoui.Context) {
	t := c.Theme()
	a.card(c, "无损缓存清理（移入回收备份目录，不破坏登录态）", func() {
		mygoui.Row(c).Gap(10).Children(func() {
			mygoui.Text(c, fmt.Sprintf("已扫描安全缓存总计：%s", maintenance.FormatBytes(a.cacheTotalBytes))).Bold().Grow(1)
			if mygoui.Button(c, "重新扫描").Clicked() {
				a.refreshAsync()
			}
			if mygoui.PrimaryButton(c, "清理选中项").Disabled(a.cacheTotalBytes == 0).Clicked() {
				movedBytes, movedCount, errs := maintenance.CleanSelected(a.cacheItems)
				if len(errs) > 0 {
					a.lastCleanMsg = fmt.Sprintf("已移走 %d 项 (%s)，部分文件被占用: %s", movedCount, maintenance.FormatBytes(movedBytes), errs[0])
				} else {
					a.lastCleanMsg = fmt.Sprintf("已安全清理 %d 项，释放 %s", movedCount, maintenance.FormatBytes(movedBytes))
				}
				a.refreshAsync()
			}
		})
		if a.lastCleanMsg != "" {
			mygoui.Text(c, a.lastCleanMsg).FontSize(12).TextColor(t.Accent)
		}
		mygoui.Divider(c)
		if len(a.cacheItems) == 0 {
			mygoui.Text(c, "未发现可清理的缓存目录").FontSize(12).TextColor(t.TextMuted)
			return
		}
		for i := range a.cacheItems {
			item := &a.cacheItems[i]
			mygoui.Row(c.Key(item.Name)).Gap(8).Children(func() {
				mygoui.Checkbox(c, &item.Selected, item.Name).Grow(1)
				mygoui.Text(c, maintenance.FormatBytes(item.Bytes)).FontSize(12).TextColor(t.TextMuted)
			})
		}
	})
}

func (a *App) settingsPage(c *mygoui.Context) {
	t := c.Theme()
	a.card(c, a.t("settings.language.title", "显示语言"), func() {
		mygoui.Text(c, a.t("settings.language.subtitle", "同时决定 GravityBox 与 Antigravity 的界面语言")).FontSize(12).TextColor(t.TextMuted)
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
		mygoui.Row(c).Gap(10).Children(func() {
			mygoui.Text(c, "启动时最小化到系统托盘").Grow(1)
			if mygoui.Switch(c, &a.cfg.StartInTray).Label("启动时最小化到系统托盘").Changed() {
				a.saveConfig()
			}
		})
	})
}

func (a *App) aboutPage(c *mygoui.Context) {
	t := c.Theme()
	a.card(c, "关于 GravityBox (MyGo Native 版)", func() {
		mygoui.Text(c, "版本：v1.2.0").FontSize(13).Bold()
		mygoui.Text(c, "架构：Go + github.com/egoist/mygo (Direct3D 11 GPU 原生绘制)").FontSize(13)
		mygoui.Text(c, "特性：零 Node.js 子进程、零 WebView2 进程、零 CGO 依赖").FontSize(13).TextColor(t.TextMuted)
		mygoui.Text(c, fmt.Sprintf("数据目录：%s", config.StateDir())).FontSize(12).TextColor(t.TextMuted)
	})
}

func (a *App) card(c *mygoui.Context, title string, body func()) {
	t := c.Theme()
	mygoui.Column(c).Padding(16).Gap(10).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
		mygoui.Text(c, title).FontSize(15).Bold()
		body()
	})
}

func (a *App) kvRow(c *mygoui.Context, label, value string) {
	t := c.Theme()
	mygoui.Row(c).Gap(12).Children(func() {
		mygoui.Text(c, label).FontSize(13).TextColor(t.TextMuted).Grow(1)
		mygoui.Text(c, value).FontSize(13)
	})
}

func (a *App) connectionBadge() string {
	if !a.antigravityRunning {
		return "未运行"
	}
	if a.cdpStatus.Windows > 0 {
		return fmt.Sprintf("已连接 · %d 窗口", a.cdpStatus.Windows)
	}
	if a.cdpStatus.Mode == "debug-port-missing" {
		return "运行中 · 无调试端口"
	}
	return "运行中"
}

func (a *App) asarSummary() string {
	if !a.asarStatus.AppFound {
		return "未检测到 app.asar"
	}
	if a.asarStatus.IsPatched {
		return fmt.Sprintf("已深度汉化 (%s)", orDash(a.asarStatus.TagVersion))
	}
	if a.asarStatus.LegacyPatched {
		return "检测到旧版 ASAR 汉化痕迹"
	}
	return "官方原版（未写入磁盘补丁）"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func langLabel(code string) string {
	switch code {
	case "en":
		return "English"
	case "zh-Hans":
		return "简体中文"
	default:
		return "跟随系统"
	}
}
