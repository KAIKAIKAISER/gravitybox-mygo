# GravityBox MyGo 重写架构规划（Windows 核心闭环）

本项目是使用 **Pure Go + MyGo Native UI (`egoist/mygo`)** 对 [KAIKAIKAISER/gravitybox](https://github.com/KAIKAIKAISER/gravitybox) 进行的纯原生轻量化重写，彻底移除 `node.exe` 后端子进程与 `WebView2` 浏览器渲染层。

## 为什么选 MyGo Native UI
- **零 Node.js**：原 `src/*.ts` 后端逻辑迁入 Go 协程，编译为单文件原生可执行程序。
- **零 WebView2**：Windows 界面由 `github.com/egoist/mygo/ui` 直接通过 Direct3D 11 在 GPU 绘制，不拉起 `msedgewebview2.exe`。
- **零 CGO**：Win32 凭据管理 (`CredReadW`/`CredWriteW`/`CredDeleteW`)、进程探测及本地 SQLite (`mygo/plugins/sqlite`) 均无需 C 编译器和 PowerShell 子进程。
- **保留注入层**：`resources/runtime/*.js` 作为注入 Antigravity IDE Chromium 隔离世界的脚本，继续以 `//go:embed` 内嵌于二进制中。

## 模块映射与阶段规划

| 原模块 (`gravitybox/src/`) | 新模块 (`gravitybox-mygo/internal/`) | Phase 1 状态 |
| :--- | :--- | :--- |
| `platform.ts` / `files.ts` | `internal/config` + `internal/win32` | 骨架与 Win32 原生 API 已建立 |
| `i18n.ts` | `internal/i18n` | 已接入内嵌 `zh-Hans.json` / `en.json` |
| `asar.ts` + `asar-patcher.ts` | `internal/asar` | ASAR 解析与状态检测已建立；补丁写入待完善 |
| `cdp.ts` + `discovery.ts` + `controller.ts` | `internal/cdp` | Loopback Target 发现与 CDP WebSocket 客户端已建立；注入循环与词典下发待接入 |
| `hud/hud-bridge.ts` + `hud/metrics-engine.ts` | `internal/hud` | 待迁移（Phase 1 下一步） |
| `accounts.ts` + `account-switch.ts` + `account-quota.ts` | `internal/accounts` + `internal/quota` | 待迁移（结合 `mygo/plugins/sqlite`） |
| `maintenance.ts` + `maintenance-catalog.ts` | `internal/maintenance` | 安全缓存扫描与回收站移动骨架已建立 |
| `native-win/` + `ui/` | `internal/ui` + `main.go` | Pure MyGo 托盘 + 快捷浮窗 + 5 页主控制台骨架已建立 |
