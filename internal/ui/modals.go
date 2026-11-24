package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"gravitybox-mygo/internal/accounts"
	"gravitybox-mygo/internal/maintenance"
	"gravitybox-mygo/internal/usage"
)

func (a *App) renderModals(c *mygoui.Context) {
	t := c.Theme()

	// 1. Add Account Modal
	mygoui.Modal(c, &a.showAddAccountModal, func() {
		mygoui.Column(c).Width(420).Gap(12).Children(func() {
			mygoui.Text(c, "添加账号").FontSize(16).Bold()
			if a.liveQuota.Connected {
				hasLiveCred := false
				for _, acc := range a.savedAccounts {
					if strings.EqualFold(acc.Email, a.liveQuota.Email) && acc.HasCredentialSnapshot {
						hasLiveCred = true
						break
					}
				}
				msg := fmt.Sprintf("检测到当前 Antigravity 已登录：%s (%s)。点击下方按钮保存凭据快照，后续可随时一键切换。", a.liveQuota.Email, a.liveQuota.Tier)
				if hasLiveCred {
					msg = fmt.Sprintf("当前账号 %s (%s) 已保存过登录凭据快照。点击下方按钮可更新为最新登录状态与配额。", a.liveQuota.Email, a.liveQuota.Tier)
				}
				mygoui.Text(c, msg).FontSize(13).TextColor(t.TextMuted)
			} else {
				mygoui.Text(c, "请先打开 Antigravity 并登录目标账号，再回到此处点击「保存当前登录账号」建立可切换快照。").
					FontSize(13).TextColor(t.TextMuted)
			}
			mygoui.Row(c).Gap(8).Justify(mygoui.End).Children(func() {
				if mygoui.Button(c, "关闭").Clicked() {
					a.showAddAccountModal = false
				}
				if mygoui.PrimaryButton(c, "保存当前登录账号").Disabled(!a.liveQuota.Connected || a.acctBusy.Load()).Clicked() {
					a.showAddAccountModal = false
					a.snapshotCurrentAccountAsync()
				}
			})
		})
	})

	// 2. Smart Switch Config Modal
	mygoui.Modal(c, &a.showAutoSwitchModal, func() {
		mygoui.Column(c).Width(380).Gap(12).Children(func() {
			mygoui.Text(c, "配置智能切换").FontSize(16).Bold()
			mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "触发提醒阈值 (%)").Grow(1)
				mygoui.TextInput(c.Key("as-thresh"), &a.autoSwitchThreshText).Width(90)
			})
			mygoui.Row(c).Gap(10).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "候选最低配额 (%)").Grow(1)
				mygoui.TextInput(c.Key("as-target"), &a.autoSwitchTargetText).Width(90)
			})
			mygoui.Row(c).Gap(8).Justify(mygoui.End).Children(func() {
				if mygoui.Button(c, "取消").Clicked() {
					a.showAutoSwitchModal = false
				}
				if mygoui.PrimaryButton(c, "保存").Clicked() {
					if v, err := strconv.ParseFloat(strings.TrimSpace(a.autoSwitchThreshText), 64); err == nil && v > 0 && v < 100 {
						a.autoSwitch.ThresholdPct = v
					}
					if v, err := strconv.ParseFloat(strings.TrimSpace(a.autoSwitchTargetText), 64); err == nil && v > 0 && v <= 100 {
						a.autoSwitch.TargetPct = v
					}
					_ = accounts.SaveAutoSwitch(a.autoSwitch)
					a.showAutoSwitchModal = false
				}
			})
		})
	})

	// 3. Rename Account Modal
	mygoui.Modal(c, &a.showRenameModal, func() {
		mygoui.Column(c).Width(360).Gap(12).Children(func() {
			mygoui.Text(c, "重命名账号").FontSize(16).Bold()
			mygoui.Text(c, a.renameTargetEmail).FontSize(12).TextColor(t.TextMuted)
			submitted := mygoui.TextInput(c.Key("rename-input"), &a.renameInput).AutoFocus().Submitted()
			mygoui.Row(c).Gap(8).Justify(mygoui.End).Children(func() {
				if mygoui.Button(c, "取消").Clicked() {
					a.showRenameModal = false
				}
				if submitted || mygoui.PrimaryButton(c, "保存").Clicked() {
					_ = accounts.RenameAccount(a.renameTargetEmail, a.renameInput)
					a.showRenameModal = false
					a.refreshAsync()
				}
			})
		})
	})

	// 4. Delete Account Modal
	mygoui.Modal(c, &a.showDeleteModal, func() {
		mygoui.Column(c).Width(360).Gap(12).Children(func() {
			mygoui.Text(c, "删除账号快照").FontSize(16).Bold()
			mygoui.Text(c, fmt.Sprintf("确认删除 %s 的本地快照与凭据吗？", a.deleteTargetEmail)).FontSize(13).TextColor(t.TextMuted)
			mygoui.Row(c).Gap(8).Justify(mygoui.End).Children(func() {
				if mygoui.Button(c, "取消").Clicked() {
					a.showDeleteModal = false
				}
				if mygoui.PrimaryButton(c, "确认删除").Clicked() {
					_ = accounts.DeleteAccount(a.deleteTargetEmail)
					a.showDeleteModal = false
					a.refreshAsync()
				}
			})
		})
	})

	// 5. API Pricing Modal
	mygoui.Modal(c, &a.showPricingModal, func() {
		mygoui.Column(c).Width(560).Gap(12).Children(func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "API 定价基准 (USD / 1M Tokens)").FontSize(16).Bold().Grow(1)
				if mygoui.Button(c, "关闭").Clicked() {
					a.showPricingModal = false
				}
			})
			mygoui.Scroll(c).Height(340).Gap(6).Children(func() {
				for _, p := range a.pricingEntries {
					mygoui.Row(c.Key("pr-"+p.Model)).Gap(8).Padding(4, 0).Children(func() {
						mygoui.Text(c, p.Model).FontSize(12).Bold().Grow(1)
						mygoui.Text(c, fmt.Sprintf("输入 $%.2f · 缓存 $%.2f · 输出 $%.2f", p.InputPerM, p.CacheReadM, p.OutputPerM)).
							FontSize(12).TextColor(t.TextMuted)
					})
				}
			})
		})
	})

	// 6. Request Events Modal
	mygoui.Modal(c, &a.showEventsModal, func() {
		mygoui.Column(c).Width(620).Gap(12).Children(func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "近期请求明细").FontSize(16).Bold().Grow(1)
				if mygoui.Button(c, "关闭").Clicked() {
					a.showEventsModal = false
				}
			})
			mygoui.Scroll(c).Height(360).Gap(6).Children(func() {
				if len(a.usageSummary.RecentEvents) == 0 {
					mygoui.Text(c, "当前范围暂无请求记录").FontSize(12).TextColor(t.TextMuted)
					return
				}
				for idx, ev := range a.usageSummary.RecentEvents {
					mygoui.Row(c.Key(fmt.Sprintf("ev-%d-%d", idx, ev.Timestamp))).Gap(10).Padding(4, 0).Children(func() {
						mygoui.Text(c, time.Unix(ev.Timestamp, 0).Format("01-02 15:04")).FontSize(11).TextColor(t.TextMuted).Width(84)
						mygoui.Text(c, ev.Model).FontSize(12).Bold().Grow(1)
						mygoui.Text(c, fmt.Sprintf("%s tok · %s", usage.FormatTokens(ev.TotalTokens), usage.FormatUSD(ev.EstimatedUSD))).FontSize(12)
					})
				}
			})
		})
	})

	// 7. Storage & Cache Maintenance Modal
	mygoui.Modal(c, &a.showStorageModal, func() {
		mygoui.Column(c).Width(520).Gap(12).Children(func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, "存储与维护").FontSize(16).Bold().Grow(1)
				if mygoui.Button(c, "关闭").Clicked() {
					a.showStorageModal = false
				}
			})
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, fmt.Sprintf("可安全释放缓存：%s", maintenance.FormatBytes(a.cacheTotalBytes))).Bold().Grow(1)
				if mygoui.Button(c, "重新扫描").Clicked() {
					a.refreshAsync()
				}
				if mygoui.PrimaryButton(c, "清理选中项").Disabled(a.cacheTotalBytes == 0).Clicked() {
					movedBytes, movedCount, errs := maintenance.CleanSelected(a.cacheItems)
					if len(errs) > 0 {
						a.lastCleanMsg = fmt.Sprintf("已移走 %d 项 (%s)，部分文件被占用: %s", movedCount, maintenance.FormatBytes(movedBytes), errs[0])
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
			mygoui.Scroll(c).Height(280).Gap(6).Children(func() {
				if len(a.cacheItems) == 0 {
					mygoui.Text(c, "未发现可清理的缓存目录").FontSize(12).TextColor(t.TextMuted)
					return
				}
				for i := range a.cacheItems {
					item := &a.cacheItems[i]
					mygoui.Row(c.Key("mcache-"+item.Name)).Gap(8).AlignItems(mygoui.Center).Children(func() {
						mygoui.Checkbox(c, &item.Selected, item.Name).Grow(1)
						mygoui.Text(c, maintenance.FormatBytes(item.Bytes)).FontSize(12).TextColor(t.TextMuted)
					})
				}
			})
		})
	})

	// 8. Scan Untranslated Strings Result Modal
	mygoui.Modal(c, &a.showScanModal, func() {
		mygoui.Column(c).Width(560).Gap(12).Children(func() {
			mygoui.Row(c).AlignItems(mygoui.Center).Children(func() {
				mygoui.Text(c, fmt.Sprintf("扫描结果 · v%s (%d 条)", a.scanResult.AppVersion, a.scanResult.TotalFound)).FontSize(16).Bold().Grow(1)
				if mygoui.Button(c, "复制列表").Disabled(len(a.scanResult.Pending) == 0).Clicked() {
					mygo.Clipboard.WriteText(strings.Join(a.scanResult.Pending, "\n"))
					a.statusNotice = "已复制待译文本列表到剪贴板"
				}
				if mygoui.Button(c, "关闭").Clicked() {
					a.showScanModal = false
				}
			})
			mygoui.Scroll(c).Height(320).Gap(4).Children(func() {
				for i, s := range a.scanResult.Pending {
					mygoui.Text(c.Key(fmt.Sprintf("scan-%d", i)), s).FontSize(12)
				}
			})
		})
	})
}
