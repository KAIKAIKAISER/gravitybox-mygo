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
	"github.com/egoist/mygo/ui/chart"

	"gravitybox-mygo/internal/accounts"
	"gravitybox-mygo/internal/asar"
	"gravitybox-mygo/internal/cdp"
	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/i18n"
	"gravitybox-mygo/internal/maintenance"
	"gravitybox-mygo/internal/quota"
	"gravitybox-mygo/internal/usage"
	"gravitybox-mygo/internal/win32"
	"gravitybox-mygo/resources"
)

var (
	svgOverview = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>`))
	svgAccounts = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>`))
	svgUsage    = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 20V10"/><path d="M12 20V4"/><path d="M6 20v-6"/></svg>`))
	svgLocale   = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m5 8 6 6"/><path d="m4 14 6-6 2-3"/><path d="M2 5h12"/><path d="M7 2h1"/><path d="m22 22-5-10-5 10"/><path d="M14 18h6"/></svg>`))
	svgSettings = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9c.26.6.77 1 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>`))
	svgAbout    = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>`))
)

type navItem struct {
	id       string
	key      string
	fallback string
	icon     *mygoui.SVG
}

var navItems = []navItem{
	{id: "overview", key: "nav.overview", fallback: "总览", icon: svgOverview},
	{id: "accounts", key: "nav.accounts", fallback: "账号", icon: svgAccounts},
	{id: "usage", key: "nav.usage", fallback: "用量", icon: svgUsage},
	{id: "localization", key: "nav.localization", fallback: "深度汉化", icon: svgLocale},
	{id: "settings", key: "nav.settings", fallback: "设置", icon: svgSettings},
	{id: "about", key: "nav.about", fallback: "关于", icon: svgAbout},
}

// App holds all state for GravityBox windows, tray, and background services.
type App struct {
	mainWin    *mygo.Window
	popoverWin *mygo.Window
	tray       *mygo.Tray
	quitting   bool

	logoBmp   *mygoui.Bitmap
	modelBmps map[string]*mygoui.Bitmap

	cfg          config.Config
	activeNav    string
	selectedLang string
	usagePeriod  int // 0: 今日, 1: 累计

	controller *cdp.Controller
	refreshing atomic.Bool
	asarBusy   atomic.Bool
	acctBusy   atomic.Bool
	relaunch   atomic.Bool

	// Live probe state
	antigravityRunning bool
	appDir             string
	asarStatus         asar.Status
	cdpStatus          cdp.ControllerStatus
	liveQuota          quota.LiveStatus
	usageSummary       usage.Summary
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

// Run initializes and runs the GravityBox application.
func Run() error {
	cfg := config.Load()
	ctrl, err := cdp.NewController(cfg.DebugPort, i18n.ResolveLocale(cfg.AppLanguage), cfg.TranslationEnabled, cfg.HudEnabled)
	if err != nil {
		return err
	}
	app := &App{
		cfg:          cfg,
		activeNav:    "overview",
		selectedLang: langLabel(cfg.AppLanguage),
		controller:   ctrl,
		modelBmps:    make(map[string]*mygoui.Bitmap),
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

func (a *App) loadBitmaps() {
	if raw, err := resources.ReadFile("icon.png"); err == nil {
		if bmp, err := mygoui.DecodeBitmap(raw); err == nil {
			a.logoBmp = bmp
		}
	}
	for _, key := range []string{"gemini", "claude", "openai"} {
		if raw, err := resources.ReadFile("assets/model-icons/" + key + ".png"); err == nil {
			if bmp, err := mygoui.DecodeBitmap(raw); err == nil {
				a.modelBmps[key] = bmp
			}
		}
	}
}

func (a *App) onReady() {
	a.loadBitmaps()
	iconBytes, _ := resources.ReadFile("icon.png")

	var err error
	a.tray, err = mygo.NewTray(mygo.TrayOptions{
		Icon:    iconBytes,
		ToolTip: "GravityBox",
	})
	if err != nil {
		log.Fatal("tray init failed: ", err)
	}

	a.mainWin = mygo.NewWindow(mygo.WindowOptions{
		Title:     "GravityBox",
		Width:     980,
		Height:    680,
		MinWidth:  820,
		MinHeight: 560,
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
		Title:             "GravityBox",
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
			Click: func(*mygo.MenuItem, *mygo.Window) { a.showMain("overview") },
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

		uCtx, uCancel := context.WithTimeout(context.Background(), 3*time.Second)
		uSum := usage.LoadSummary(uCtx)
		uCancel()

		curID, savedList, _ := accounts.ListSaved()
		items, total := maintenance.ScanSafeCaches()

		mygo.RunOnMain(func() {
			a.appDir = appDir
			a.antigravityRunning = running
			a.asarStatus = asarSt
			a.cdpStatus = cdpSt
			a.liveQuota = liveQ
			a.usageSummary = uSum
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

func (a *App) showMain(nav string) {
	if nav != "" {
		a.activeNav = nav
	}
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
	a.statusNotice = fmt.Sprintf("已启动 Antigravity (调试端口 %d)", a.cfg.DebugPort)
	a.invalidateVisible()
	a.refreshAsync()
}

func (a *App) safeRelaunchAsync() {
	if !a.relaunch.CompareAndSwap(false, true) {
		return
	}
	a.statusNotice = "正在安全关闭并重启 Antigravity…"
	a.invalidateVisible()
	go func() {
		defer a.relaunch.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = win32.StopAntigravityGracefully(ctx)
		cancel()

		dir := config.DetectAntigravityInstallDir(a.cfg.AppPath)
		var msg string
		if dir == "" {
			msg = "未找到 Antigravity 安装目录"
		} else if err := win32.LaunchAntigravity(filepath.Join(dir, "Antigravity.exe"), a.cfg.DebugPort); err != nil {
			msg = fmt.Sprintf("重启失败: %v", err)
		} else {
			msg = "已安全重启 Antigravity，正在连接调试端口…"
		}
		mygo.RunOnMain(func() {
			a.statusNotice = msg
			a.invalidateVisible()
		})
		time.Sleep(1500 * time.Millisecond)
		a.refreshAsync()
	}()
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
	a.accountNotice = "正在保存当前登录账号快照…"
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
				a.accountNotice = fmt.Sprintf("已保存 %s 快照", email)
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
	a.accountNotice = fmt.Sprintf("正在切换至 %s…", email)
	a.invalidateVisible()
	go func() {
		defer a.acctBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := accounts.SwitchAccount(ctx, email, a.appDir, a.cfg.DebugPort)
		cancel()
		mygo.RunOnMain(func() {
			if err != nil {
				a.accountNotice = fmt.Sprintf("切换失败: %v", err)
			} else {
				a.accountNotice = fmt.Sprintf("已切换至 %s", email)
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
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			if a.logoBmp != nil {
				mygoui.Image(c, a.logoBmp).Size(20, 20).Fit(mygoui.Contain).Radius(4)
			}
			mygoui.Text(c, "GravityBox").FontSize(15).Bold().Grow(1)
			mygoui.Badge(c, a.sidebarStatusText())
		})
		if a.liveQuota.Connected {
			mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, a.liveQuota.Email).FontSize(12).TextColor(t.TextMuted).Grow(1)
				mygoui.Badge(c, a.liveQuota.Tier)
			})
			for _, w := range a.liveQuota.Windows {
				a.quotaBarRow(c, w)
			}
		}
		mygoui.Divider(c)
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "IDE 中文翻译").Grow(1)
			if mygoui.Switch(c, &a.cfg.TranslationEnabled).Label("IDE 中文翻译").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "对话状态栏 (HUD)").Grow(1)
			if mygoui.Switch(c, &a.cfg.HudEnabled).Label("对话状态栏").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Divider(c)
		mygoui.Text(c, fmt.Sprintf("今日用量：%s Token · %s", usage.FormatTokens(a.usageSummary.TodayTokens), usage.FormatUSD(a.usageSummary.TodayUSD))).FontSize(12).TextColor(t.TextMuted)
		mygoui.Spacer(c)
		mygoui.Row(c).Gap(8).Children(func() {
			if mygoui.Button(c, a.t("sidebar.open_antigravity", "打开 Antigravity")).Clicked() {
				a.launchAntigravity()
			}
			mygoui.Spacer(c)
			if mygoui.PrimaryButton(c, "打开主界面 ↗").Clicked() {
				a.showMain("overview")
			}
		})
	})
}

func (a *App) mainView(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Row(c).Fill().Background(t.Background).Children(func() {
		a.sidebarView(c)
		mygoui.Scroll(c).Grow(1).Padding(24).Gap(16).Children(func() {
			switch a.activeNav {
			case "overview":
				a.overviewPage(c)
			case "accounts":
				a.accountsPage(c)
			case "usage":
				a.usagePage(c)
			case "localization":
				a.localizationPage(c)
			case "settings":
				a.settingsPage(c)
			case "about":
				a.aboutPage(c)
			}
		})
	})
}

func (a *App) sidebarView(c *mygoui.Context) {
	t := c.Theme()
	sidebarBg := mygoui.RGB(238, 240, 244)
	if t.Dark {
		sidebarBg = mygoui.RGB(26, 29, 36)
	}
	activeBg := mygoui.RGBA(37, 99, 235, 0.12)
	activeFg := mygoui.RGB(37, 99, 235)
	if t.Dark {
		activeBg = mygoui.RGBA(59, 130, 246, 0.18)
		activeFg = mygoui.RGB(96, 165, 250)
	}

	mygoui.Column(c).Width(204).FillHeight().Padding(16, 12).Gap(14).
		Background(sidebarBg).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		// Brand Row
		mygoui.Row(c).Padding(4, 6).Gap(8).AlignItems(mygoui.Center).Children(func() {
			if a.logoBmp != nil {
				mygoui.Image(c, a.logoBmp).Size(24, 24).Fit(mygoui.Contain).Radius(5)
			}
			mygoui.Text(c, "GravityBox").FontSize(16).Bold()
		})

		// Navigation items
		mygoui.Column(c).Gap(3).Grow(1).Children(func() {
			for _, item := range navItems {
				item := item
				selected := a.activeNav == item.id
				row := mygoui.Row(c.Key("nav-"+item.id)).Padding(8, 10).Gap(10).Radius(8).AlignItems(mygoui.Center).Cursor(mygoui.CursorPointer)
				if selected {
					row.Background(activeBg)
				}
				row.OnClick(func() {
					a.activeNav = item.id
					a.mainWin.Invalidate()
				})
				row.Children(func() {
					fg := t.TextMuted
					if selected {
						fg = activeFg
					}
					mygoui.Icon(c, item.icon).FontSize(15).TextColor(fg)
					label := mygoui.Text(c, a.t(item.key, item.fallback)).FontSize(13)
					if selected {
						label.Bold().TextColor(activeFg)
					} else {
						label.TextColor(t.Text)
					}
				})
			}
		})

		// Footer status + launch button
		mygoui.Column(c).Gap(8).Children(func() {
			dotColor := mygoui.RGB(156, 163, 175)
			if a.cdpStatus.Windows > 0 {
				dotColor = mygoui.RGB(22, 163, 74)
			} else if a.antigravityRunning {
				dotColor = mygoui.RGB(217, 119, 6)
			}
			mygoui.Row(c).Padding(7, 10).Gap(8).Radius(8).AlignItems(mygoui.Center).Background(t.Surface).Border(1, t.Border).Children(func() {
				mygoui.Box(c).Size(8, 8).Radius(4).Background(dotColor)
				mygoui.Text(c, a.sidebarStatusText()).FontSize(12).TextColor(t.TextMuted).Grow(1)
			})
			if mygoui.Button(c, a.t("sidebar.open_antigravity", "打开 Antigravity")).FillWidth().Clicked() {
				a.launchAntigravity()
			}
		})
	})
}

func (a *App) overviewPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
		mygoui.Text(c, a.t("overview.title", "总览")).FontSize(22).Bold().Grow(1)
	})

	// Debug port missing warning banner (matches upstream amber banner)
	if a.antigravityRunning && a.cdpStatus.Mode == "debug-port-missing" {
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

	// 2-Column Bottom Bento (Hourly Chart + Active Models)
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
			a.hourlyBarChart(c, a.usageSummary.TodayHourly)
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
					mygoui.Meter(c, pct, 0, 100, nil).Label(m.Model)
					mygoui.Text(c, fmt.Sprintf("%d req · %.0f%% · %s", m.RequestCount, pct, usage.FormatUSD(m.EstimatedUSD))).
						FontSize(11).TextColor(t.TextMuted)
				})
			}
		})
	})
}

func (a *App) hourlyBarChart(c *mygoui.Context, hourly [24]int64) {
	t := c.Theme()
	labels := make([]string, 24)
	vals := make([]float64, 24)
	for i := 0; i < 24; i++ {
		labels[i] = fmt.Sprintf("%02d时", i)
		vals[i] = float64(hourly[i])
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
	for _, h := range []int{0, 4, 8, 12, 16, 20, 23} {
		xTicks = append(xTicks, chart.Tick{At: x.At(h), Label: labels[h]})
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
				Tooltip(fmt.Sprintf("%s: %s Token", labels[i], usage.FormatTokens(hourly[i])))
		}
	}).Label("今日时段消耗").Height(150).Gap(6)
}

func (a *App) quotaBarRow(c *mygoui.Context, w quota.QuotaWindow) {
	t := c.Theme()
	mygoui.Column(c.Key("qw-"+w.Label)).Gap(5).Children(func() {
		mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
			if bmp := a.modelBmps[w.IconKey]; bmp != nil {
				mygoui.Image(c, bmp).Size(15, 15).Fit(mygoui.Contain).Radius(3)
			}
			mygoui.Text(c, w.Label).FontSize(13).Bold().Grow(1)
			if w.ResetCountdown != "" {
				mygoui.Text(c, w.ResetCountdown).FontSize(12).TextColor(t.TextMuted)
			}
			pctText := fmt.Sprintf("%.0f%%", w.RemainingPct)
			txt := mygoui.Text(c, pctText).FontSize(13).Bold()
			if w.RemainingPct <= 10 {
				txt.TextColor(mygoui.RGB(220, 38, 38))
			}
		})
		mygoui.Meter(c, w.RemainingPct, 0, 100, &mygoui.MeterLevels{
			Warning:       20,
			Critical:      10,
			NormalColor:   mygoui.RGB(37, 99, 235),
			WarningColor:  mygoui.RGB(217, 119, 6),
			CriticalColor: mygoui.RGB(220, 38, 38),
		}).Label(w.Label)
	})
}

func (a *App) metricCard(c *mygoui.Context, title, value, sub string) {
	t := c.Theme()
	a.card(c, "", func() {
		mygoui.Text(c, title).FontSize(12).TextColor(t.TextMuted)
		mygoui.Text(c, value).FontSize(26).Bold()
		mygoui.Text(c, sub).FontSize(12).TextColor(t.TextMuted)
	})
}

func (a *App) accountsPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Text(c, a.t("nav.accounts", "账号")).FontSize(22).Bold()

	a.card(c, "当前登录账号", func() {
		if !a.liveQuota.Connected {
			msg := a.liveQuota.Error
			if msg == "" {
				msg = "未检测到运行中的 Antigravity 登录账号"
			}
			mygoui.Text(c, msg).FontSize(13).TextColor(t.TextMuted)
		} else {
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, a.liveQuota.Email).FontSize(15).Bold().Grow(1)
				mygoui.Badge(c, a.liveQuota.Tier)
				if mygoui.PrimaryButton(c, "保存当前账号快照").Disabled(a.acctBusy.Load()).Clicked() {
					a.snapshotCurrentAccountAsync()
				}
			})
			if len(a.liveQuota.Windows) > 0 {
				mygoui.Grid(c).Columns(2).GapX(20).GapY(10).Children(func() {
					for _, w := range a.liveQuota.Windows {
						a.quotaBarRow(c, w)
					}
				})
			}
		}
		if a.accountNotice != "" {
			mygoui.Text(c, a.accountNotice).FontSize(12).TextColor(t.Accent)
		}
	})

	a.card(c, "已保存账号", func() {
		if len(a.savedAccounts) == 0 {
			mygoui.Text(c, "暂无已保存账号快照；请在 Antigravity 登录后点击上方「保存当前账号快照」。").FontSize(12).TextColor(t.TextMuted)
			return
		}
		for _, acc := range a.savedAccounts {
			acc := acc
			mygoui.Row(c.Key("saved-"+acc.Email)).Gap(10).AlignItems(mygoui.Center).Padding(6, 0).Children(func() {
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

func (a *App) usagePage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
		mygoui.Text(c, a.t("nav.usage", "用量")).FontSize(22).Bold().Grow(1)
		mygoui.Tabs(c, &a.usagePeriod, "今日", "累计全部")
	})

	tok := a.usageSummary.TodayTokens
	usd := a.usageSummary.TodayUSD
	req := a.usageSummary.TodayRequests
	models := a.usageSummary.TodayModels
	if a.usagePeriod == 1 {
		tok = a.usageSummary.TotalTokens
		usd = a.usageSummary.TotalUSD
		req = a.usageSummary.TotalRequests
		models = a.usageSummary.AllModels
	}

	mygoui.Grid(c).Columns(3).Gap(12).Children(func() {
		a.metricCard(c, "Token 总量", usage.FormatTokens(tok), "来自本地用量账本")
		a.metricCard(c, "费用估算", usage.FormatUSD(usd), "按公开模型定价核算")
		a.metricCard(c, "请求次数", fmt.Sprintf("%d", req), "已记录生成次数")
	})

	a.card(c, "模型用量明细", func() {
		if len(models) == 0 {
			mygoui.Text(c, "暂无本地用量记录").FontSize(12).TextColor(t.TextMuted)
			return
		}
		baseTotal := max(int64(1), tok)
		for _, m := range models {
			pct := float64(m.TotalTokens) * 100 / float64(baseTotal)
			mygoui.Column(c.Key("um-"+m.Model)).Gap(4).Padding(6, 0).Children(func() {
				mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
					mygoui.Text(c, m.Model).Bold().Grow(1)
					mygoui.Text(c, fmt.Sprintf("%s Token · %d 次 · %s", usage.FormatTokens(m.TotalTokens), m.RequestCount, usage.FormatUSD(m.EstimatedUSD))).FontSize(12)
				})
				mygoui.Meter(c, pct, 0, 100, nil).Label(m.Model)
			})
		}
	})

	a.card(c, "缓存维护", func() {
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, fmt.Sprintf("可安全清理运行时缓存：%s", maintenance.FormatBytes(a.cacheTotalBytes))).Bold().Grow(1)
			if mygoui.Button(c, "重新扫描").Clicked() {
				a.refreshAsync()
			}
			if mygoui.PrimaryButton(c, "清理选中项").Disabled(a.cacheTotalBytes == 0).Clicked() {
				movedBytes, movedCount, errs := maintenance.CleanSelected(a.cacheItems)
				if len(errs) > 0 {
					a.lastCleanMsg = fmt.Sprintf("已清理 %d 项 (%s)，部分文件被占用: %s", movedCount, maintenance.FormatBytes(movedBytes), errs[0])
				} else {
					a.lastCleanMsg = fmt.Sprintf("已清理 %d 项，释放 %s", movedCount, maintenance.FormatBytes(movedBytes))
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
			mygoui.Row(c.Key("cache-"+item.Name)).Gap(8).AlignItems(mygoui.Center).Children(func() {
				mygoui.Checkbox(c, &item.Selected, item.Name).Grow(1)
				mygoui.Text(c, maintenance.FormatBytes(item.Bytes)).FontSize(12).TextColor(t.TextMuted)
			})
		}
	})
}

func (a *App) localizationPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Text(c, "深度汉化").FontSize(22).Bold()

	a.card(c, "应用级 ASAR 汉化与连接状态", func() {
		a.kvRow(c, "安装路径", orDash(a.appDir))
		a.kvRow(c, "官方客户端版本", orDash(a.asarStatus.AppVersion))
		a.kvRow(c, "ASAR 汉化状态", a.asarSummary())
		a.kvRow(c, "运行时词库", fmt.Sprintf("%d 条 (%s)", a.cdpStatus.DictionaryEntries, a.cdpStatus.DictionaryVersion))
		a.kvRow(c, "已注入窗口数", fmt.Sprintf("%d", a.cdpStatus.Windows))
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
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "启用 IDE 界面中文翻译").Grow(1)
			if mygoui.Switch(c, &a.cfg.TranslationEnabled).Label("启用 IDE 界面中文翻译").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "启用输入框对话状态栏 (HUD)").Grow(1)
			if mygoui.Switch(c, &a.cfg.HudEnabled).Label("启用输入框对话状态栏").Changed() {
				a.saveConfig()
			}
		})
	})
}

func (a *App) settingsPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Text(c, a.t("settings.title", "设置")).FontSize(22).Bold()

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
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "启动时最小化到系统托盘").Grow(1)
			if mygoui.Switch(c, &a.cfg.StartInTray).Label("启动时最小化到系统托盘").Changed() {
				a.saveConfig()
			}
		})
	})

	a.card(c, "功能开关", func() {
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "IDE 中文翻译").Grow(1)
			if mygoui.Switch(c, &a.cfg.TranslationEnabled).Label("IDE 中文翻译").Changed() {
				a.saveConfig()
			}
		})
		mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
			mygoui.Text(c, "对话状态栏 (HUD)").Grow(1)
			if mygoui.Switch(c, &a.cfg.HudEnabled).Label("对话状态栏").Changed() {
				a.saveConfig()
			}
		})
	})
}

func (a *App) aboutPage(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Text(c, a.t("nav.about", "关于")).FontSize(22).Bold()
	a.card(c, "GravityBox", func() {
		mygoui.Row(c).Gap(12).AlignItems(mygoui.Center).Children(func() {
			if a.logoBmp != nil {
				mygoui.Image(c, a.logoBmp).Size(44, 44).Fit(mygoui.Contain).Radius(10)
			}
			mygoui.Column(c).Gap(2).Children(func() {
				mygoui.Text(c, "GravityBox").FontSize(16).Bold()
				mygoui.Text(c, "版本 v1.2.1").FontSize(12).TextColor(t.TextMuted)
			})
		})
		mygoui.Divider(c)
		a.kvRow(c, "数据目录", config.StateDir())
		a.kvRow(c, "Antigravity 目录", orDash(a.appDir))
		a.kvRow(c, "开源协议", "MIT")
	})
}

func (a *App) card(c *mygoui.Context, title string, body func()) {
	t := c.Theme()
	mygoui.Column(c).Padding(16).Gap(12).Radius(12).Background(t.Surface).Border(1, t.Border).Children(func() {
		if title != "" {
			mygoui.Text(c, title).FontSize(14).Bold()
		}
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

func (a *App) sidebarStatusText() string {
	if !a.antigravityRunning {
		return "等待 Antigravity 启动…"
	}
	if a.cdpStatus.Windows > 0 {
		return fmt.Sprintf("已连接 · %d 窗口", a.cdpStatus.Windows)
	}
	return "检测到 Antigravity 正在运行…"
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
