# GravityBox

Google Antigravity 桌面伴侣工具。

## 功能
- **界面汉化与状态栏**：支持应用级 `app.asar` 深度汉化、还原官方原版，以及运行时中文适配与对话状态栏（HUD）控制。
- **配额监控**：实时读取当前账号 5 小时与每周模型配额水位与重置倒计时。
- **多账号管理**：保存当前登录账号快照并在已保存账号间切换。
- **用量统计**：读取本地用量账本，展示今日与累计 Token、费用估算、请求次数与时段分布。
- **缓存维护**：扫描并安全清理 Antigravity 运行时缓存目录。

## 构建
```bash
go test ./...
go tool mygo build -platform windows/amd64
```
