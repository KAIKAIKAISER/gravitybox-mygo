package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"

	"gravitybox-mygo/internal/accounts"
	"gravitybox-mygo/internal/asar"
	"gravitybox-mygo/internal/cdp"
	"gravitybox-mygo/internal/config"
	"gravitybox-mygo/internal/hud"
	"gravitybox-mygo/internal/i18n"
	"gravitybox-mygo/internal/maintenance"
	"gravitybox-mygo/internal/quota"
	"gravitybox-mygo/internal/usage"
	"gravitybox-mygo/internal/win32"
	"gravitybox-mygo/resources"
)

const appVersion = "1.2.11"

var (
	svgOverview    = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>`))
	svgAccounts    = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>`))
	svgUsage       = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 20V10"/><path d="M12 20V4"/><path d="M6 20v-6"/></svg>`))
	svgLocale      = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m5 8 6 6"/><path d="m4 14 6-6 2-3"/><path d="M2 5h12"/><path d="M7 2h1"/><path d="m22 22-5-10-5 10"/><path d="M14 18h6"/></svg>`))
	svgSettings    = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9c.26.6.77 1 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>`))
	svgAbout       = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>`))
	svgSwitch      = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/></svg>`))
	svgRefresh     = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/></svg>`))
	svgGlobe       = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>`))
	svgHud         = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M7 15h4"/><path d="M13 15h4"/></svg>`))
	svgDollar      = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="1" x2="12" y2="23"/><path d="M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"/></svg>`))
	svgBolt        = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>`))
	svgLink        = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>`))
	svgStorage     = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg>`))
	svgChevronUp   = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m18 15-6-6-6 6"/></svg>`))
	svgChevronDown = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>`))
	svgPalette     = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="13.5" cy="6.5" r=".5" fill="currentColor"/><circle cx="17.5" cy="10.5" r=".5" fill="currentColor"/><circle cx="8.5" cy="7.5" r=".5" fill="currentColor"/><circle cx="6.5" cy="12.5" r=".5" fill="currentColor"/><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.926 0 1.648-.746 1.648-1.688 0-.437-.18-.835-.437-1.125-.29-.289-.438-.652-.438-1.125a1.64 1.64 0 0 1 1.668-1.668h1.996c3.051 0 5.555-2.503 5.555-5.554C21.965 6.012 17.461 2 12 2z"/></svg>`))
	svgGemini      = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 2c.4 4.9 4.1 8.6 9 9-4.9.4-8.6 4.1-9 9-.4-4.9-4.1-8.6-9-9 4.9-.4 8.6-4.1 9-9Z"/></svg>`))
	svgClaude      = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><line x1="12" y1="3" x2="12" y2="21"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="5.6" y1="5.6" x2="18.4" y2="18.4"/><line x1="18.4" y1="5.6" x2="5.6" y2="18.4"/></svg>`))
	svgOpenAI      = mygoui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/></svg>`))
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
	sawRunning bool

	logoBmp   *mygoui.Bitmap
	modelBmps map[string]*mygoui.Bitmap

	cfg              config.Config
	autoSwitch       accounts.AutoSwitchConfig
	activeNav        string
	popoverTab          int    // 0: 用量, 1: 配额, 2: 账号
	popoverPeriod       int    // 0: 今日, 1: 近 7 天, 2: 近 30 天, 3: 全部
	selectedLang        string
	selectedInterval    string
	selectedAppTheme    string
	selectedCodexPreset string
	selectedAccent      string
	selectedChatFont    string
	selectedHudLayout   string
	selectedCacheMode   string
	selectedCtxLimit    string
	usagePeriod         int    // 0: 今日, 1: 近 7 天, 2: 近 30 天, 3: 全部
	usageAcctFilter     string // "全部账号" or email

	// Collapse state on Accounts page
	allCollapsed      bool
	collapsedAccounts map[string]bool

	controller *cdp.Controller
	hudBridge  *hud.Bridge
	refreshing atomic.Bool
	asarBusy   atomic.Bool
	acctBusy   atomic.Bool
	relaunch   atomic.Bool
	syncBusy   atomic.Bool
	updBusy    atomic.Bool

	// Live probe state
	antigravityRunning bool
	appDir             string
	asarStatus         asar.Status
	zhPackInfo         asar.ZhPackInfo
	cdpStatus          cdp.ControllerStatus
	liveQuota          quota.LiveStatus
	usageSummary       usage.Summary
	popoverUsage       usage.Summary
	currentAccountID   string
	savedAccounts      []accounts.SavedAccount
	lastProbeAt        time.Time
	statusNotice       string
	accountNotice      string
	updateNotice       string
	latestVersion      string

	// Maintenance state
	cacheItems      []maintenance.CacheItem
	cacheTotalBytes int64
	lastCleanMsg    string

	// Modal flags & fields
	showAddAccountModal  bool
	showAutoSwitchModal  bool
	autoSwitchThreshText string
	autoSwitchTargetText string
	showRenameModal      bool
	renameTargetEmail    string
	renameInput          string
	showDeleteModal      bool
	deleteTargetEmail    string
	showPricingModal     bool
	pricingEntries       []usage.PriceEntry
	showEventsModal      bool
	showStorageModal     bool
	showScanModal        bool
	scanResult           asar.ScanResult
}

	// Run initializes and runs the GravityBox application.
func Run() error {
	cfg := config.Load()
	ctrl, err := cdp.NewController(cfg.DebugPort, i18n.ResolveLocale(cfg.AppLanguage), cfg.TranslationEnabled, cfg.HudEnabled)
	if err != nil {
		return err
	}
	hudEngine := hud.NewMetricsEngine(config.StateDir())
	hudBridge := hud.NewBridge(hudEngine)
	hudBridge.UpdateTelemetryConfig(cfg.TelemetryLayout, cfg.TelemetryCacheMode, cfg.ManualContextLimit)
	ctrl.SetHudHandler(hudBridge)
	ctrl.UpdateCodexSettings(cdp.CodexSettings{
		ThemeEnabled:       cfg.CodexThemeEnabled,
		ThemePreset:        cfg.CodexThemePreset,
		TelemetryLayout:    cfg.TelemetryLayout,
		TelemetryCacheMode: cfg.TelemetryCacheMode,
		ManualContextLimit: cfg.ManualContextLimit,
		ThemeVars:          config.EffectiveCodexThemeVars(cfg),
	})
	asCfg := accounts.LoadAutoSwitch()
	app := &App{
		cfg:                  cfg,
		autoSwitch:           asCfg,
		activeNav:            "overview",
		popoverTab:           1, // 配额 / 总览直观展示
		selectedLang:         langLabel(cfg.AppLanguage),
		selectedInterval:     intervalLabel(cfg.QuotaRefreshInterval),
		selectedAppTheme:     appThemeLabel(cfg.AppTheme),
		selectedCodexPreset:  codexPresetLabel(cfg.CodexThemePreset),
		selectedAccent:       accentLabel(cfg.AccentColor),
		selectedChatFont:     chatFontLabel(cfg.ChatFontSize),
		selectedHudLayout:    hudLayoutLabel(cfg.TelemetryLayout),
		selectedCacheMode:    cacheModeLabel(cfg.TelemetryCacheMode),
		selectedCtxLimit:     ctxLimitLabel(cfg.ManualContextLimit),
		usageAcctFilter:      "全部账号",
		collapsedAccounts:    make(map[string]bool),
		controller:           ctrl,
		hudBridge:            hudBridge,
		modelBmps:            make(map[string]*mygoui.Bitmap),
		autoSwitchThreshText: fmt.Sprintf("%.0f", asCfg.ThresholdPct),
		autoSwitchTargetText: fmt.Sprintf("%.0f", asCfg.TargetPct),
		updateNotice:         "已是最新版本",
	}

	mygo.App.SetName("GravityBox")
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		app.quitting = true
		hudBridge.Stop()
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

func makeCrispTrayIcon(raw []byte) []byte {
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	dst := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return raw
	}
	return out.Bytes()
}

func (a *App) onReady() {
	a.loadBitmaps()
	iconBytes, _ := resources.ReadFile("icon.png")
	trayBytes := makeCrispTrayIcon(iconBytes)

	var err error
	a.tray, err = mygo.NewTray(mygo.TrayOptions{
		Icon:    trayBytes,
		ToolTip: "GravityBox",
	})
	if err != nil {
		log.Fatal("tray init failed: ", err)
	}

	a.mainWin = mygo.NewWindow(mygo.WindowOptions{
		Title:     "GravityBox",
		Width:     1040,
		Height:    720,
		MinWidth:  740,
		MinHeight: 480,
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
		Width:             388,
		Height:            520,
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
	if a.cfg.AutoCheckUpdates {
		go a.checkUpdatesAsync()
	}
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
		packInfo := asar.GetZhPackInfo(config.StateDir())

		a.controller.UpdateConfig(
			a.cfg.DebugPort,
			i18n.ResolveLocale(a.cfg.AppLanguage),
			a.cfg.TranslationEnabled,
			a.cfg.HudEnabled,
			asarSt.IsPatched,
		)
		a.controller.UpdateCodexSettings(cdp.CodexSettings{
			ThemeEnabled:       a.cfg.CodexThemeEnabled,
			ThemePreset:        a.cfg.CodexThemePreset,
			TelemetryLayout:    a.cfg.TelemetryLayout,
			TelemetryCacheMode: a.cfg.TelemetryCacheMode,
			ManualContextLimit: a.cfg.ManualContextLimit,
			ThemeVars:          config.EffectiveCodexThemeVars(a.cfg),
		})
		if a.hudBridge != nil {
			a.hudBridge.UpdateTelemetryConfig(a.cfg.TelemetryLayout, a.cfg.TelemetryCacheMode, a.cfg.ManualContextLimit)
		}
		cdpCtx, cdpCancel := context.WithTimeout(context.Background(), 4*time.Second)
		cdpSt := a.controller.SyncOnce(cdpCtx, running)
		cdpCancel()

		var liveQ quota.LiveStatus
		if running {
			qCtx, qCancel := context.WithTimeout(context.Background(), 4*time.Second)
			liveQ = quota.ProbeLive(qCtx)
			qCancel()
			if liveQ.Connected && liveQ.Email != "" {
				usage.SetActiveAccountEmail(liveQ.Email)
			}
		}

		acctFilter := a.usageAcctFilter
		if acctFilter == "全部账号" || acctFilter == "" {
			acctFilter = "all"
		}
		uCtx, uCancel := context.WithTimeout(context.Background(), 8*time.Second)
		uSum := usage.LoadSummary(uCtx, a.usagePeriod, acctFilter)
		popSum := uSum
		if a.popoverPeriod != a.usagePeriod || acctFilter != "all" {
			popSum = usage.LoadSummary(uCtx, a.popoverPeriod, "all")
		}
		uCancel()

		curID, savedList, _ := accounts.ListSaved()
		items, total := maintenance.ScanSafeCaches()

		if a.cfg.FollowAntigravity {
			if running {
				a.sawRunning = true
			} else if a.sawRunning {
				mygo.RunOnMain(func() { mygo.App.Quit() })
				return
			}
		}

		mygo.RunOnMain(func() {
			a.appDir = appDir
			a.antigravityRunning = running
			a.asarStatus = asarSt
			a.zhPackInfo = packInfo
			a.cdpStatus = cdpSt
			a.liveQuota = liveQ
			a.usageSummary = uSum
			a.popoverUsage = popSum
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
	a.refreshAsync()
}

func (a *App) launchAntigravity() {
	dir := config.DetectAntigravityInstallDir(a.cfg.AppPath)
	exe := ""
	if dir != "" {
		exe = filepath.Join(dir, "Antigravity.exe")
	}
	if err := win32.OpenAntigravity(exe, a.cfg.DebugPort); err != nil {
		a.statusNotice = fmt.Sprintf("唤起 Antigravity 失败: %v", err)
		a.invalidateVisible()
		return
	}
	a.statusNotice = ""
	a.refreshAsync()
}

func (a *App) safeRelaunchAsync() {
	if !a.relaunch.CompareAndSwap(false, true) {
		return
	}
	a.statusNotice = "正在关闭并安全重启 Antigravity…"
	a.invalidateVisible()
	go func() {
		defer a.relaunch.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = win32.StopAntigravityGracefully(ctx)
		cancel()

		time.Sleep(450 * time.Millisecond)
		dir := config.DetectAntigravityInstallDir(a.cfg.AppPath)
		var msg string
		if dir == "" {
			msg = "未找到 Antigravity 安装目录"
		} else if err := win32.LaunchAntigravity(filepath.Join(dir, "Antigravity.exe"), a.cfg.DebugPort); err != nil {
			msg = fmt.Sprintf("重启失败: %v", err)
		} else {
			msg = "已重启 Antigravity，正在连接调试端口…"
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
	a.statusNotice = "正在应用深度汉化并热生效…"
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

func (a *App) syncZhPackAsync() {
	if !a.syncBusy.CompareAndSwap(false, true) {
		return
	}
	a.statusNotice = "正在从 GitHub 同步最新汉化包…"
	a.invalidateVisible()
	go func() {
		defer a.syncBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		msg, err := asar.SyncRemoteZhPack(ctx, config.StateDir())
		cancel()
		mygo.RunOnMain(func() {
			if err != nil {
				a.statusNotice = err.Error()
			} else {
				a.statusNotice = msg
			}
			a.invalidateVisible()
		})
		a.refreshAsync()
	}()
}

func (a *App) scanNewStringsAsync() {
	if !a.syncBusy.CompareAndSwap(false, true) {
		return
	}
	a.statusNotice = "正在扫描当前版本未译英文文本…"
	a.invalidateVisible()
	go func() {
		defer a.syncBusy.Store(false)
		res, err := asar.ScanUntranslatedStrings(a.appDir, config.StateDir())
		mygo.RunOnMain(func() {
			if err != nil {
				a.statusNotice = err.Error()
			} else {
				a.scanResult = res
				a.showScanModal = true
				a.statusNotice = fmt.Sprintf("扫描完成：发现 %d 条未覆盖文本", res.TotalFound)
			}
			a.invalidateVisible()
		})
	}()
}

func (a *App) snapshotCurrentAccountAsync() {
	if !a.acctBusy.CompareAndSwap(false, true) {
		return
	}
	email := a.liveQuota.Email
	name := a.liveQuota.Name
	tier := a.liveQuota.Tier
	families := append([]quota.FamilyQuota(nil), a.liveQuota.Families...)
	a.accountNotice = "正在保存当前登录账号…"
	a.invalidateVisible()
	go func() {
		defer a.acctBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := accounts.SnapshotCurrentFromVscdb(ctx, email, name, tier, families...)
		cancel()
		mygo.RunOnMain(func() {
			if err != nil {
				a.accountNotice = err.Error()
			} else {
				a.accountNotice = fmt.Sprintf("已保存当前账号 %s", email)
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

func (a *App) checkPricingAsync() {
	a.statusNotice = "正在同步最新模型价格基准…"
	a.invalidateVisible()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		msg, err := usage.CheckRemotePricing(ctx)
		cancel()
		mygo.RunOnMain(func() {
			if err != nil {
				a.statusNotice = fmt.Sprintf("同步失败: %v", err)
			} else {
				a.statusNotice = msg
			}
			a.invalidateVisible()
		})
	}()
}

func (a *App) checkUpdatesAsync() {
	if !a.updBusy.CompareAndSwap(false, true) {
		return
	}
	mygo.RunOnMain(func() {
		a.updateNotice = "正在检查更新…"
		a.invalidateVisible()
	})
	go func() {
		defer a.updBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/KAIKAIKAISER/gravitybox-mygo/releases/latest", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			mygo.RunOnMain(func() {
				a.updateNotice = "检查更新失败，请检查网络"
				a.invalidateVisible()
			})
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		_ = resp.Body.Close()
		var rel struct {
			TagName string `json:"tag_name"`
		}
		_ = json.Unmarshal(raw, &rel)
		latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
		msg := "已是最新版本"
		if latest != "" && latest != appVersion {
			msg = fmt.Sprintf("发现新版本 v%s", latest)
		}
		mygo.RunOnMain(func() {
			a.latestVersion = latest
			a.updateNotice = msg
			a.invalidateVisible()
		})
	}()
}

func (a *App) saveConfig() {
	_ = config.Save(a.cfg)
	a.refreshAsync()
}

func (a *App) resolveAppTheme(c *mygoui.Context) *mygoui.Theme {
	isDark := c.Theme().Dark
	switch a.cfg.AppTheme {
	case "light":
		isDark = false
	case "dark":
		isDark = true
	}
	base := mygoui.LightTheme()
	if isDark {
		base = mygoui.DarkTheme()
	}
	var accent mygoui.Color
	switch a.cfg.AccentColor {
	case "emerald":
		accent = mygoui.RGB(5, 150, 105)
	case "indigo":
		accent = mygoui.RGB(99, 102, 241)
	case "amber":
		accent = mygoui.RGB(217, 119, 6)
	default:
		if isDark {
			accent = mygoui.RGB(44, 103, 197)
		} else {
			accent = mygoui.RGB(43, 127, 255)
		}
	}
	return base.WithAccent(accent)
}

func (a *App) pageCanvasColor(t *mygoui.Theme) mygoui.Color {
	if t.Dark {
		return mygoui.RGB(24, 24, 24)
	}
	return mygoui.RGB(249, 249, 249)
}

func (a *App) popoverView(c *mygoui.Context) {
	c.SetTheme(a.resolveAppTheme(c))
	if c.Shortcut(0, mygoui.KeyEscape) {
		a.popoverWin.Hide()
	}
	t := c.Theme()
	bg := a.pageCanvasColor(t)
	cardBg := mygoui.RGB(255, 255, 255)
	if t.Dark {
		cardBg = mygoui.RGB(35, 35, 35)
	}

	mygoui.Column(c).Fill().Padding(14).Gap(10).Background(bg).Border(1, t.Border).Children(func() {
		// Header
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			if a.logoBmp != nil {
				mygoui.Image(c, a.logoBmp).Size(20, 20).Fit(mygoui.Contain).Radius(5)
			}
			mygoui.Text(c, "GravityBox").FontSize(14).Bold().NoWrap().Grow(1)
			a.pillBadge(c, a.sidebarStatusText(), mygoui.RGBA(15, 23, 42, 0.06), t.TextMuted)
		})

		// 3-tab selector matching upstream popover
		if mygoui.Segmented(c, &a.popoverTab, "用量", "配额", "账号").FillEqually().Label("面板分页").Changed() {
			a.refreshAsync()
		}

		// Scrollable middle pane so footer never clips!
		mygoui.Scroll(c).Grow(1).FillWidth().Children(func() {
			mygoui.Column(c).FillWidth().Gap(10).Children(func() {
				switch a.popoverTab {
				case 0:
					if mygoui.Segmented(c, &a.popoverPeriod, "今日", "近 7 天", "近 30 天", "全部").FillEqually().Label("周期").Changed() {
						a.refreshAsync()
					}
					mygoui.Grid(c).Columns(2).Gap(8).Children(func() {
						a.miniTile(c, cardBg, "Token 用量", usage.FormatTokens(a.popoverUsage.PeriodTokens))
						a.miniTile(c, cardBg, "请求数", fmt.Sprintf("%d", a.popoverUsage.PeriodRequests))
						a.miniTile(c, cardBg, "API 费用估算", usage.FormatUSD(a.popoverUsage.PeriodUSD))
						a.miniTile(c, cardBg, "缓存命中率", fmt.Sprintf("%.1f%%", a.popoverUsage.CacheHitRate))
					})
					if len(a.popoverUsage.Models) > 0 {
						mygoui.Column(c).Padding(10, 12).Gap(6).Radius(10).Background(cardBg).Border(1, t.Border).Children(func() {
							limit := min(3, len(a.popoverUsage.Models))
							for i := 0; i < limit; i++ {
								m := a.popoverUsage.Models[i]
								mygoui.Row(c.Key("pm-"+m.Model)).Gap(6).AlignItems(mygoui.Center).Children(func() {
									mygoui.Text(c, m.Model).FontSize(12).Bold().NoWrap().Grow(1)
									mygoui.Text(c, usage.FormatTokens(m.TotalTokens)).FontSize(12).TextColor(t.TextMuted).NoWrap()
								})
							}
						})
					}
				case 1:
					if a.liveQuota.Connected {
						mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
							mygoui.Text(c, a.liveQuota.Email).FontSize(12).TextColor(t.TextMuted).NoWrap().Grow(1)
							if a.liveQuota.Tier != "" {
								a.pillBadge(c, a.liveQuota.Tier, mygoui.RGBA(37, 99, 235, 0.12), mygoui.RGB(37, 99, 235))
							}
						})
						mygoui.Column(c).Padding(10, 12).Gap(10).Radius(10).Background(cardBg).Border(1, t.Border).Children(func() {
							for _, w := range a.liveQuota.Windows {
								a.quotaBarRow(c, w)
							}
						})
					} else {
						mygoui.Column(c).Padding(12).Radius(10).Background(cardBg).Border(1, t.Border).Children(func() {
							mygoui.Text(c, "启动并登录 Antigravity 后将显示实时配额").FontSize(12).TextColor(t.TextMuted)
						})
					}
					mygoui.Column(c).Padding(10, 12).Gap(8).Radius(10).Background(cardBg).Border(1, t.Border).Children(func() {
						mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
							mygoui.Text(c, "IDE 中文翻译").FontSize(12).Grow(1)
							if mygoui.Switch(c, &a.cfg.TranslationEnabled).Label("IDE 中文翻译").Changed() {
								a.saveConfig()
							}
						})
						mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
							mygoui.Text(c, "对话状态栏").FontSize(12).Grow(1)
							if mygoui.Switch(c, &a.cfg.HudEnabled).Label("对话状态栏").Changed() {
								a.saveConfig()
							}
						})
						mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
							mygoui.Text(c, "Codex 昼夜主题").FontSize(12).Grow(1)
							if mygoui.Switch(c, &a.cfg.CodexThemeEnabled).Label("Codex 昼夜主题").Changed() {
								a.saveConfig()
							}
						})
					})
				case 2:
					if len(a.savedAccounts) == 0 {
						mygoui.Column(c).Padding(12).Radius(10).Background(cardBg).Border(1, t.Border).Children(func() {
							mygoui.Text(c, "暂无已保存账号快照，请在主界面「账号」页保存。").FontSize(12).TextColor(t.TextMuted)
						})
					} else {
						for _, acc := range a.savedAccounts {
							acc := acc
							isCur := a.liveQuota.Connected && acc.Email == a.liveQuota.Email
							mygoui.Row(c.Key("pop-acc-"+acc.Email)).Padding(9, 12).Gap(8).Radius(10).
								Background(cardBg).Border(1, t.Border).AlignItems(mygoui.Center).Children(func() {
								mygoui.Column(c).Grow(1).Gap(1).Children(func() {
									mygoui.Text(c, acc.Email).FontSize(12).Bold().NoWrap()
									if acc.SubscriptionTier != "" {
										mygoui.Text(c, acc.SubscriptionTier).FontSize(11).TextColor(t.TextMuted)
									}
								})
								if isCur {
									a.pillBadge(c, "当前", mygoui.RGBA(37, 99, 235, 0.12), mygoui.RGB(37, 99, 235))
								} else if mygoui.Button(c, "切换").Disabled(a.acctBusy.Load()).Clicked() {
									a.switchAccountAsync(acc.Email)
								}
							})
						}
					}
				}
			})
		})

		// Sticky Footer
		mygoui.Divider(c)
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
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

func (a *App) miniTile(c *mygoui.Context, cardBg mygoui.Color, label, value string) {
	t := c.Theme()
	mygoui.Column(c).Padding(10, 12).Gap(4).Radius(10).Background(cardBg).Border(1, t.Border).Children(func() {
		mygoui.Text(c, label).FontSize(11).TextColor(t.TextMuted).NoWrap()
		mygoui.Text(c, value).FontSize(16).Bold().NoWrap()
	})
}

func (a *App) mainView(c *mygoui.Context) {
	c.SetTheme(a.resolveAppTheme(c))
	t := c.Theme()
	bg := a.pageCanvasColor(t)
	mygoui.Row(c).Fill().AlignItems(mygoui.Stretch).Background(bg).Children(func() {
		a.sidebarView(c)
		mygoui.Scroll(c).Grow(1).FillHeight().Children(func() {
			mygoui.Column(c).FillWidth().Padding(22, 24).Gap(16).Children(func() {
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
	})
	a.renderModals(c)
}

func (a *App) sidebarView(c *mygoui.Context) {
	t := c.Theme()
	sidebarBg := mygoui.RGB(242, 243, 245)
	if t.Dark {
		sidebarBg = mygoui.RGB(35, 35, 35)
	}
	activeFg := t.Accent
	activeBg := mygoui.RGBA(activeFg.R, activeFg.G, activeFg.B, 0.12)
	if t.Dark {
		activeBg = mygoui.RGBA(activeFg.R, activeFg.G, activeFg.B, 0.18)
	}
	chipBg := mygoui.RGBA(15, 23, 42, 0.05)
	if t.Dark {
		chipBg = mygoui.RGBA(255, 255, 255, 0.07)
	}

	mygoui.Column(c).Width(204).FillHeight().Padding(16, 12).Gap(14).
		Background(sidebarBg).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		mygoui.Row(c).Padding(4, 8).Gap(9).AlignItems(mygoui.Center).Children(func() {
			if a.logoBmp != nil {
				mygoui.Image(c, a.logoBmp).Size(22, 22).Fit(mygoui.Contain).Radius(5)
			}
			mygoui.Text(c, "GravityBox").FontSize(15).Bold().NoWrap()
		})

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
					label := mygoui.Text(c, a.t(item.key, item.fallback)).FontSize(13).NoWrap()
					if selected {
						label.Bold().TextColor(activeFg)
					} else {
						label.TextColor(t.Text)
					}
				})
			}
		})

		mygoui.Column(c).Gap(8).Children(func() {
			dotColor := mygoui.RGB(156, 163, 175)
			if a.cdpStatus.Windows > 0 {
				dotColor = mygoui.RGB(22, 163, 74)
			} else if a.antigravityRunning {
				dotColor = mygoui.RGB(217, 119, 6)
			}
			mygoui.Row(c).Padding(7, 10).Gap(8).Radius(8).AlignItems(mygoui.Center).Background(chipBg).Children(func() {
				mygoui.Box(c).Size(8, 8).Radius(4).Background(dotColor)
				mygoui.Text(c, a.sidebarStatusText()).FontSize(11).TextColor(t.TextMuted).NoWrap().Grow(1)
			})
			if mygoui.Button(c, a.t("sidebar.open_antigravity", "打开 Antigravity")).FillWidth().Clicked() {
				a.launchAntigravity()
			}
		})
	})
}

func (a *App) sidebarStatusText() string {
	if !a.antigravityRunning {
		return "等待 Antigravity 启动…"
	}
	if a.cdpStatus.Windows > 0 {
		if a.asarStatus.IsPatched {
			return "深度汉化已接管 · HUD 运行中"
		}
		return fmt.Sprintf("已连接 · %d 窗口", a.cdpStatus.Windows)
	}
	if a.asarStatus.IsPatched {
		return "深度汉化已接管 · 运行中"
	}
	return "检测到 Antigravity 运行中…"
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

func intervalLabel(sec int) string {
	switch sec {
	case 60:
		return "每 1 分钟"
	case 300:
		return "每 5 分钟"
	case 900:
		return "每 15 分钟"
	case 1800:
		return "每 30 分钟"
	default:
		return "手动刷新"
	}
}

func intervalFromLabel(s string) int {
	switch s {
	case "每 1 分钟":
		return 60
	case "每 5 分钟":
		return 300
	case "每 15 分钟":
		return 900
	case "每 30 分钟":
		return 1800
	default:
		return 0
	}
}

func appThemeLabel(code string) string {
	switch code {
	case "light":
		return "浅色"
	case "dark":
		return "深色"
	default:
		return "跟随系统"
	}
}

func appThemeFromLabel(s string) string {
	switch s {
	case "浅色":
		return "light"
	case "深色":
		return "dark"
	default:
		return "system"
	}
}

func codexPresetLabel(code string) string {
	switch code {
	case "codex-pure-white":
		return "Codex 纯白浅色"
	case "codex-dark":
		return "Codex 炭灰深色"
	default:
		return "自动跟随 IDE"
	}
}

func codexPresetFromLabel(s string) string {
	switch s {
	case "Codex 纯白浅色":
		return "codex-pure-white"
	case "Codex 炭灰深色":
		return "codex-dark"
	default:
		return "auto"
	}
}

func accentLabel(code string) string {
	switch code {
	case "emerald":
		return "翡翠绿"
	case "indigo":
		return "靛青紫"
	case "amber":
		return "琥珀金"
	default:
		return "经典蓝"
	}
}

func accentFromLabel(s string) string {
	switch s {
	case "翡翠绿":
		return "emerald"
	case "靛青紫":
		return "indigo"
	case "琥珀金":
		return "amber"
	default:
		return "blue"
	}
}

func chatFontLabel(px int) string {
	switch px {
	case 14:
		return "14px"
	case 15:
		return "15px"
	case 16:
		return "16px"
	default:
		return "默认"
	}
}

func chatFontFromLabel(s string) int {
	switch s {
	case "14px":
		return 14
	case "15px":
		return 15
	case "16px":
		return 16
	default:
		return 0
	}
}

func hudLayoutLabel(code string) string {
	if code == "detailed" {
		return "详细 (含 TTFT/Out)"
	}
	return "紧凑 (默认)"
}

func hudLayoutFromLabel(s string) string {
	if s == "详细 (含 TTFT/Out)" {
		return "detailed"
	}
	return "compact"
}

func cacheModeLabel(code string) string {
	if code == "step" {
		return "当前步命中率"
	}
	return "会话累计命中率"
}

func cacheModeFromLabel(s string) string {
	if s == "当前步命中率" {
		return "step"
	}
	return "session"
}

func ctxLimitLabel(v int) string {
	switch v {
	case 256000:
		return "256k"
	case 512000:
		return "512k"
	case 1000000:
		return "1M"
	case 2000000:
		return "2M"
	default:
		return "自动识别"
	}
}

func ctxLimitFromLabel(s string) int {
	switch s {
	case "256k":
		return 256000
	case "512k":
		return 512000
	case "1M":
		return 1000000
	case "2M":
		return 2000000
	default:
		return 0
	}
}
