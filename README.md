# GravityBox (MyGo Native 版)

基于 **Pure Go + [MyGo](https://github.com/egoist/mygo) Native UI** 重写的 Google Antigravity 伴侣应用。

## 核心特点
- **零 Node.js**：原 TypeScript 后端全部迁移为原生 Go 协程，打包无需内嵌 `node.exe`。
- **零 WebView2 / Electron**：桌面主控制台与托盘快捷浮窗使用 `mygo/ui` 通过 GPU（Windows Direct3D 11 / macOS Metal）原生绘制，彻底告别 `msedgewebview2.exe` 内存占用。
- **零 CGO**：Windows 凭据管理（`CredReadW`/`CredWriteW`/`CredDeleteW`）、Toolhelp32 进程枚举、SQLite 读写（`mygo/plugins/sqlite`）均无 CGO 依赖，支持跨平台编译。

## Phase 1 已实现能力
1. **ASAR 深度汉化与还原**：解析与重打包 Antigravity `app.asar`（含 SHA-256 完整性校验与 `.bak` 双备份），支持一键写入与恢复原版。
2. **CDP 隔离世界注入与 HUD**：自动探测本地 `127.0.0.1:9222` 调试窗口，创建 `GRAVITYBOX-localization-v1` 隔离世界注入运行时中文引擎与对话状态栏（HUD）。
3. **实时配额与安全切号**：直连本地 `language_server` 获取当前账号与配额水位，并通过 Windows 凭据管理器 + `state.vscdb` 事务实现账号快照与安全切换。
4. **无损缓存维护**：扫描 Antigravity UserData 安全缓存目录，非破坏性移入维护回收目录。

## 本地测试与构建
```bash
go test ./...
go tool mygo build -platform windows/amd64
```
