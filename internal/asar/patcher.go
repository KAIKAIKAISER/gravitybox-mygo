package asar

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gravitybox-mygo/internal/cdp"
	"gravitybox-mygo/resources"
)

const (
	asarIntegrityBlockSize = 4 * 1024 * 1024
	defaultTagPath         = "dist/antigravity_zh_cn.tag"
)

// ZhPackSpec mirrors assets/zh-pack.json.
type ZhPackSpec struct {
	Schema          int               `json:"schema"`
	AppVersion      string            `json:"appVersion"`
	UpdatedAt       string            `json:"updatedAt"`
	EntriesCount    int               `json:"entriesCount"`
	Whitelist       []string          `json:"whitelist"`
	Entries         map[string]string `json:"entries"`
	RuntimeTemplate string            `json:"runtimeTemplate"`
	ElectronPatches struct {
		MarkerStart    string            `json:"markerStart"`
		MarkerEnd      string            `json:"markerEnd"`
		SubframeStart  string            `json:"subframeStart"`
		SubframeEnd    string            `json:"subframeEnd"`
		MenuDictionary map[string]string `json:"menuDictionary"`
		TrayDictionary map[string]string `json:"trayDictionary"`
		Replacements   []struct {
			File  string          `json:"file"`
			Pairs [][]any         `json:"pairs"`
		} `json:"replacements"`
	} `json:"electronPatches"`
}

type asarIntegrity struct {
	Algorithm string   `json:"algorithm"`
	Hash      string   `json:"hash"`
	BlockSize int      `json:"blockSize"`
	Blocks    []string `json:"blocks"`
}

type writeEntry struct {
	Size       *int64         `json:"size,omitempty"`
	Offset     string         `json:"offset,omitempty"`
	Unpacked   bool           `json:"unpacked,omitempty"`
	Link       string         `json:"link,omitempty"`
	Executable bool           `json:"executable,omitempty"`
	Integrity  *asarIntegrity `json:"integrity,omitempty"`
	Files      map[string]any `json:"files,omitempty"`
}

type copyPlanItem struct {
	buf       []byte
	srcOffset int64
	size      int64
}

// ApplyResult summarizes a deep ASAR localization write or hot inject pass.
type ApplyResult struct {
	OK           bool
	FileLocked   bool
	PatchedFiles []string
	HotTargets   int
	Message      string
}

var (
	zhPackMu        sync.Mutex
	bundledPackOnce sync.Once
	bundledPackVal  *ZhPackSpec
	bundledPackErr  error
	zhInfoCacheSig  string
	zhInfoCacheVal  ZhPackInfo
)

func loadBundledZhPack() (*ZhPackSpec, error) {
	bundledPackOnce.Do(func() {
		raw, err := resources.ReadFile("assets/zh-pack.json")
		if err != nil {
			bundledPackErr = err
			return
		}
		var p ZhPackSpec
		if err := json.Unmarshal(raw, &p); err != nil {
			bundledPackErr = err
			return
		}
		if p.Schema != 1 || len(p.Entries) == 0 || !strings.Contains(p.RuntimeTemplate, "{{MAP_JSON}}") {
			bundledPackErr = errors.New("invalid embedded zh-pack.json")
			return
		}
		bundledPackVal = &p
	})
	return bundledPackVal, bundledPackErr
}

// LoadZhPack loads embedded assets/zh-pack.json, or a newer remote-synced override in stateDir.
func LoadZhPack(stateDir string) (*ZhPackSpec, error) {
	bundled, bundledErr := loadBundledZhPack()

	if stateDir != "" {
		if raw, err := os.ReadFile(filepath.Join(stateDir, "zh-pack-latest.json")); err == nil {
			var remote ZhPackSpec
			if json.Unmarshal(raw, &remote) == nil && remote.Schema == 1 && len(remote.Entries) > 0 && strings.Contains(remote.RuntimeTemplate, "{{MAP_JSON}}") {
				if bundled == nil || isNewerZhPack(&remote, bundled) {
					return &remote, nil
				}
			}
		}
	}

	if bundled == nil {
		return nil, bundledErr
	}
	return bundled, nil
}

func isNewerZhPack(candidate, baseline *ZhPackSpec) bool {
	ct, cErr := parsePackTime(candidate.UpdatedAt)
	bt, bErr := parsePackTime(baseline.UpdatedAt)
	if cErr == nil && bErr == nil && !ct.Equal(bt) {
		return ct.After(bt)
	}
	return len(candidate.Entries) > len(baseline.Entries)
}

func parsePackTime(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// CompileRuntime renders the standalone preload/hot-inject script from ZhPackSpec.
func CompileRuntime(pack *ZhPackSpec, hotInject bool) (string, error) {
	merged := make(map[string]string, len(pack.Entries)+8)
	maps.Copy(merged, pack.Entries)
	for _, w := range pack.Whitelist {
		delete(merged, w)
		delete(merged, strings.ToLower(w))
	}
	merged["Model"] = "模型"
	merged["model"] = "模型"
	merged["Models"] = "模型"
	merged["models"] = "模型"
	merged["MODELS"] = "模型"

	pairs := make([][2]string, 0, len(merged))
	for k, v := range merged {
		pairs = append(pairs, [2]string{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i][0]) != len(pairs[j][0]) {
			return len(pairs[i][0]) > len(pairs[j][0])
		}
		return pairs[i][0] < pairs[j][0]
	})
	mapBytes, err := json.Marshal(pairs)
	if err != nil {
		return "", err
	}

	guard := "if (window.__ANTIGRAVITY_CN_INJECTED__) return;\n    window.__ANTIGRAVITY_CN_INJECTED__ = true;"
	hotReturn := ""
	if hotInject {
		guard = ""
		hotReturn = `return "SUCCESS_TRANSLATED_" + translatedCount;`
	}
	out := strings.Replace(pack.RuntimeTemplate, "{{GUARD}}", guard, 1)
	out = strings.Replace(out, "{{MAP_JSON}}", string(mapBytes), 1)
	out = strings.Replace(out, "{{LONG_JSON}}", string(mapBytes), 1)
	out = strings.Replace(out, "{{HOT_RETURN}}", hotReturn, 1)
	return out, nil
}

// ApplyDeepLocalization patches app.asar (backing up app.asar.bak first) and hot-injects running windows.
func ApplyDeepLocalization(ctx context.Context, appDir, stateDir string, debugPort int) ApplyResult {
	st := InspectApp(appDir)
	if !st.AppFound {
		return ApplyResult{Message: "未找到 Antigravity 安装目录与 resources/app.asar"}
	}
	pack, err := LoadZhPack(stateDir)
	if err != nil {
		return ApplyResult{Message: fmt.Sprintf("加载汉化词库失败: %v", err)}
	}

	bakPath := st.AsarPath + ".bak"
	if !st.IsPatched || !st.HasBackup {
		if err := copyFile(st.AsarPath, bakPath); err != nil {
			return ApplyResult{Message: fmt.Sprintf("备份原版 app.asar 失败: %v", err)}
		}
		if stateDir != "" {
			_ = os.MkdirAll(filepath.Join(stateDir, "backups"), 0o700)
			_ = copyFile(st.AsarPath, filepath.Join(stateDir, "backups", "app.asar.original"))
		}
	}
	srcPath := st.AsarPath
	if st.HasBackup && !st.IsPatched {
		srcPath = st.AsarPath
	} else if st.HasBackup {
		// Prefer pure backup if its version matches.
		bakArc, err := Open(bakPath)
		if err == nil {
			if raw, rerr := bakArc.ReadEntry("package.json", 256*1024); rerr == nil {
				var pkg struct {
					Version string `json:"version"`
				}
				if json.Unmarshal(raw, &pkg) == nil && strings.TrimSpace(pkg.Version) == st.AppVersion {
					if _, tagErr := bakArc.ReadEntry(defaultTagPath, 1024); tagErr != nil {
						srcPath = bakPath
					}
				}
			}
			_ = bakArc.Close()
		}
	}

	srcArc, err := Open(srcPath)
	if err != nil {
		return ApplyResult{Message: fmt.Sprintf("读取源 ASAR 失败: %v", err)}
	}
	overrides := make(map[string][]byte)
	var patchedFiles []string

	runtimeStandalone, err := CompileRuntime(pack, false)
	if err != nil {
		_ = srcArc.Close()
		return ApplyResult{Message: fmt.Sprintf("编译运行时脚本失败: %v", err)}
	}

	preloadRaw, err := srcArc.ReadEntry("dist/preload.js", 15*1024*1024)
	if err != nil {
		_ = srcArc.Close()
		return ApplyResult{Message: "原 app.asar 中缺少 dist/preload.js"}
	}
	cleanPreload := strings.TrimRight(stripBetweenMarkers(string(preloadRaw), pack.ElectronPatches.MarkerStart, pack.ElectronPatches.MarkerEnd), " \t\r\n")
	overrides["dist/preload.js"] = []byte(cleanPreload + "\n\n" + runtimeStandalone)
	patchedFiles = append(patchedFiles, "dist/preload.js")

	overrides["dist/zh_translator_runtime.js"] = []byte(runtimeStandalone)
	patchedFiles = append(patchedFiles, "dist/zh_translator_runtime.js")

	if mainRaw, err := srcArc.ReadEntry("dist/main.js", 15*1024*1024); err == nil {
		mainContent := stripBetweenMarkers(string(mainRaw), pack.ElectronPatches.SubframeStart, pack.ElectronPatches.SubframeEnd)
		for _, rep := range pack.ElectronPatches.Replacements {
			if rep.File != "dist/main.js" {
				continue
			}
			mainContent = applyReplacements(mainContent, rep.Pairs)
		}
		hookTarget := "electron_1.app.on('web-contents-created', (_event, contents) => {"
		if strings.Contains(mainContent, hookTarget) {
			snippet := hookTarget + "\n    " + pack.ElectronPatches.SubframeStart + "\n" +
				`    contents.on('did-frame-finish-load', (_frameEvent, isMainFrame, frameProcessId, frameRoutingId) => {
        try {
            if (isMainFrame) return;
            const frame = electron_1.webFrameMain && electron_1.webFrameMain.fromId(frameProcessId, frameRoutingId);
            if (frame && frame.url && frame.url.startsWith('plugin://')) {
                if (!global.__zhSubframeRuntimeCache) {
                    const rtFile = path.join(__dirname, 'zh_translator_runtime.js');
                    if (fs.existsSync(rtFile)) global.__zhSubframeRuntimeCache = fs.readFileSync(rtFile, 'utf-8');
                }
                if (global.__zhSubframeRuntimeCache) {
                    frame.executeJavaScript(global.__zhSubframeRuntimeCache).catch(() => {});
                }
            }
        } catch (_) {}
    });` + "\n    " + pack.ElectronPatches.SubframeEnd
			mainContent = strings.Replace(mainContent, hookTarget, snippet, 1)
		}
		overrides["dist/main.js"] = []byte(mainContent)
		patchedFiles = append(patchedFiles, "dist/main.js")
	}

	if menuRaw, err := srcArc.ReadEntry("dist/menu.js", 15*1024*1024); err == nil {
		menuContent := stripBetweenMarkers(string(menuRaw), pack.ElectronPatches.MarkerStart, pack.ElectronPatches.MarkerEnd)
		menuDictJSON, _ := json.Marshal(pack.ElectronPatches.MenuDictionary)
		menuPatch := fmt.Sprintf("\n    %s\n    try {\n        const menuTranslations = %s;\n        function translateMenu(items) {\n            if (!items) return;\n            for (const item of items) {\n                let label = item.label || '';\n                let mnemonic = '';\n                let cleanLabel = label;\n                const m = label.match(/&([a-zA-Z])/);\n                if (m) { mnemonic = \"(&\" + m[1] + \")\"; cleanLabel = label.replace('&', ''); }\n                if (menuTranslations[cleanLabel]) item.label = menuTranslations[cleanLabel] + mnemonic;\n                else if (menuTranslations[label]) item.label = menuTranslations[label];\n                else if (/^Version\\s*([\\d\\.]*)$/i.test(cleanLabel)) {\n                    item.label = cleanLabel.replace(/^Version\\s*([\\d\\.]*)$/i, (_, v) => v ? \"版本 \" + v : \"版本\");\n                }\n                if (item.submenu && item.submenu.items) translateMenu(item.submenu.items);\n            }\n        }\n        if (!electron_1.Menu.__zh_patched) {\n            electron_1.Menu.__zh_patched = true;\n            const _origSetAppMenu = electron_1.Menu.setApplicationMenu;\n            electron_1.Menu.setApplicationMenu = function(m) {\n                if (m && m.items) translateMenu(m.items);\n                return _origSetAppMenu.call(electron_1.Menu, m);\n            };\n        }\n        translateMenu(menu.items);\n    } catch (e) {}\n    %s\n",
			pack.ElectronPatches.MarkerStart, string(menuDictJSON), pack.ElectronPatches.MarkerEnd)
		setMenuTarget := "electron_1.Menu.setApplicationMenu(menu);"
		if strings.Contains(menuContent, setMenuTarget) {
			menuContent = strings.Replace(menuContent, setMenuTarget, menuPatch+"\n    "+setMenuTarget, 1)
		}
		overrides["dist/menu.js"] = []byte(menuContent)
		patchedFiles = append(patchedFiles, "dist/menu.js")
	}

	if trayRaw, err := srcArc.ReadEntry("dist/tray.js", 15*1024*1024); err == nil {
		trayContent := strings.TrimRight(stripBetweenMarkers(string(trayRaw), pack.ElectronPatches.MarkerStart, pack.ElectronPatches.MarkerEnd), " \t\r\n")
		trayDictJSON, _ := json.Marshal(pack.ElectronPatches.TrayDictionary)
		trayPatch := fmt.Sprintf("\n%s\ntry {\n    const _origSetContextMenu = electron_1.Tray.prototype.setContextMenu;\n    const trayDict = %s;\n    function translateTrayItems(items) {\n        if (!items) return;\n        for (const it of items) {\n            let l = it.label || '';\n            if (trayDict[l]) it.label = trayDict[l];\n            else if (/^Open\\s+(.+)$/i.test(l)) it.label = l.replace(/^Open\\s+(.+)$/i, '打开 $1');\n            else if (/^(\\d+)\\s+agents?\\s+running$/i.test(l)) it.label = l.replace(/^(\\d+)\\s+agents?\\s+running$/i, '$1 个智能体运行中');\n            if (it.submenu && it.submenu.items) translateTrayItems(it.submenu.items);\n        }\n    }\n    electron_1.Tray.prototype.setContextMenu = function(menu) {\n        if (menu && menu.items) translateTrayItems(menu.items);\n        return _origSetContextMenu.call(this, menu);\n    };\n} catch(e) {}\n%s\n",
			pack.ElectronPatches.MarkerStart, string(trayDictJSON), pack.ElectronPatches.MarkerEnd)
		overrides["dist/tray.js"] = []byte(trayContent + "\n" + trayPatch)
		patchedFiles = append(patchedFiles, "dist/tray.js")
	}

	for _, rep := range pack.ElectronPatches.Replacements {
		if rep.File == "dist/main.js" {
			continue
		}
		raw, err := srcArc.ReadEntry(rep.File, 15*1024*1024)
		if err != nil {
			continue
		}
		overrides[rep.File] = []byte(applyReplacements(string(raw), rep.Pairs))
		patchedFiles = append(patchedFiles, rep.File)
	}

	tagPayload, _ := json.MarshalIndent(map[string]any{
		"schema":        1,
		"version":       st.AppVersion,
		"updatedAt":     time.Now().UTC().Format(time.RFC3339),
		"packUpdatedAt": pack.UpdatedAt,
		"source":        "GravityBox Deep Localization System",
		"entriesCount":  len(pack.Entries),
	}, "", "  ")
	overrides[defaultTagPath] = tagPayload
	patchedFiles = append(patchedFiles, defaultTagPath)
	_ = srcArc.Close()

	var fileLocked bool
	if err := patchArchive(srcPath, st.AsarPath, overrides); err != nil {
		fileLocked = true
	}

	hotTargets := HotInjectTargets(ctx, pack, debugPort)
	if fileLocked {
		return ApplyResult{
			OK:           hotTargets > 0,
			FileLocked:   true,
			PatchedFiles: patchedFiles,
			HotTargets:   hotTargets,
			Message:      fmt.Sprintf("app.asar 被占用未能覆写，但已热注入 %d 个活跃窗口；请退出 Antigravity 后重试磁盘写入。", hotTargets),
		}
	}
	return ApplyResult{
		OK:           true,
		PatchedFiles: patchedFiles,
		HotTargets:   hotTargets,
		Message:      fmt.Sprintf("已完成 v%s 深度汉化（%d 个模块），并热生效 %d 个窗口。", st.AppVersion, len(patchedFiles), hotTargets),
	}
}

// RestoreBackup restores resources/app.asar from resources/app.asar.bak.
func RestoreBackup(appDir string) ApplyResult {
	st := InspectApp(appDir)
	if !st.AppFound {
		return ApplyResult{Message: "未找到 Antigravity 资源目录"}
	}
	bakPath := st.AsarPath + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		return ApplyResult{Message: "未找到官方原版备份 (app.asar.bak)"}
	}
	if err := copyFile(bakPath, st.AsarPath); err != nil {
		return ApplyResult{FileLocked: true, Message: fmt.Sprintf("恢复失败（请先关闭 Antigravity）: %v", err)}
	}
	return ApplyResult{OK: true, Message: "已从 app.asar.bak 恢复官方原版"}
}

// HotInjectTargets injects the standalone zh-pack runtime into active CDP page/plugin iframe targets.
func HotInjectTargets(ctx context.Context, pack *ZhPackSpec, debugPort int) int {
	if debugPort <= 0 {
		debugPort = 9222
	}
	targets, err := cdp.ListTargets(ctx, debugPort)
	if err != nil {
		return 0
	}
	code, err := CompileRuntime(pack, true)
	if err != nil {
		return 0
	}
	injected := 0
	for _, tgt := range targets {
		if tgt.WebSocketDebuggerURL == "" {
			continue
		}
		if tgt.Type != "page" && !(tgt.Type == "iframe" && strings.HasPrefix(tgt.URL, "plugin://")) {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		cli, err := cdp.Dial(tctx, tgt.WebSocketDebuggerURL)
		if err == nil {
			if _, err := cli.Evaluate(tctx, code, nil); err == nil {
				injected++
			}
			_ = cli.Close()
		}
		cancel()
	}
	return injected
}

func patchArchive(srcPath, destPath string, overrides map[string][]byte) error {
	srcArc, err := Open(srcPath)
	if err != nil {
		return err
	}
	defer srcArc.Close()

	newRoot := make(map[string]any)
	var copyPlan []copyPlanItem
	used := make(map[string]bool)
	var nextOffset int64

	var walk func(srcMap map[string]Entry, destMap map[string]any, prefix string)
	walk = func(srcMap map[string]Entry, destMap map[string]any, prefix string) {
		for name, entry := range srcMap {
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			if entry.Files != nil {
				sub := make(map[string]any)
				destMap[name] = map[string]any{"files": sub}
				walk(entry.Files, sub, rel)
				continue
			}
			if entry.Unpacked || entry.Link != "" {
				destMap[name] = entry
				continue
			}
			if buf, ok := overrides[rel]; ok {
				used[rel] = true
				sz := int64(len(buf))
				destMap[name] = writeEntry{
					Size:      &sz,
					Offset:    strconv.FormatInt(nextOffset, 10),
					Integrity: computeIntegrity(buf),
				}
				copyPlan = append(copyPlan, copyPlanItem{buf: buf})
				nextOffset += sz
				continue
			}
			origSize, _ := parseNum(entry.Size)
			origOffset, _ := parseNum(entry.Offset)
			sz := origSize
			destMap[name] = writeEntry{
				Size:   &sz,
				Offset: strconv.FormatInt(nextOffset, 10),
			}
			if origSize > 0 {
				copyPlan = append(copyPlan, copyPlanItem{
					srcOffset: srcArc.dataOffset + origOffset,
					size:      origSize,
				})
			}
			nextOffset += origSize
		}
	}

	walk(srcArc.Header.Files, newRoot, "")
	for rel, buf := range overrides {
		if used[rel] {
			continue
		}
		sz := int64(len(buf))
		insertNode(newRoot, rel, writeEntry{
			Size:      &sz,
			Offset:    strconv.FormatInt(nextOffset, 10),
			Integrity: computeIntegrity(buf),
		})
		copyPlan = append(copyPlan, copyPlanItem{buf: buf})
		nextOffset += sz
	}

	jsonBuf, err := json.Marshal(map[string]any{"files": newRoot})
	if err != nil {
		return err
	}
	alignedPayload := ((4 + len(jsonBuf) + 3) / 4) * 4
	headerSize := 4 + alignedPayload
	padBytes := alignedPayload - 4 - len(jsonBuf)

	var prefix [16]byte
	binary.LittleEndian.PutUint32(prefix[0:4], 4)
	binary.LittleEndian.PutUint32(prefix[4:8], uint32(headerSize))
	binary.LittleEndian.PutUint32(prefix[8:12], uint32(alignedPayload))
	binary.LittleEndian.PutUint32(prefix[12:16], uint32(len(jsonBuf)))

	tmpPath := fmt.Sprintf("%s.tmp-%d-%d", destPath, os.Getpid(), time.Now().UnixNano())
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = out.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := out.Write(prefix[:]); err != nil {
		return err
	}
	if _, err := out.Write(jsonBuf); err != nil {
		return err
	}
	if padBytes > 0 {
		if _, err := out.Write(make([]byte, padBytes)); err != nil {
			return err
		}
	}

	scratch := make([]byte, 1024*1024)
	for _, item := range copyPlan {
		if item.buf != nil {
			if len(item.buf) > 0 {
				if _, err := out.Write(item.buf); err != nil {
					return err
				}
			}
			continue
		}
		rem := item.size
		pos := item.srcOffset
		for rem > 0 {
			chunk := int64(len(scratch))
			if rem < chunk {
				chunk = rem
			}
			n, err := srcArc.file.ReadAt(scratch[:chunk], pos)
			if err != nil && err != io.EOF {
				return err
			}
			if n == 0 {
				return errors.New("unexpected EOF reading source asar")
			}
			if _, err := out.Write(scratch[:n]); err != nil {
				return err
			}
			rem -= int64(n)
			pos += int64(n)
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	_ = out.Close()

	// Verify generated ASAR header before replacing destination.
	verArc, err := Open(tmpPath)
	if err != nil {
		return err
	}
	_ = verArc.Close()

	if err := os.Rename(tmpPath, destPath); err != nil {
		if cerr := copyFile(tmpPath, destPath); cerr != nil {
			return cerr
		}
		_ = os.Remove(tmpPath)
	}
	cleanup = false
	return nil
}

func computeIntegrity(buf []byte) *asarIntegrity {
	full := sha256.Sum256(buf)
	var blocks []string
	if len(buf) == 0 {
		blocks = []string{hex.EncodeToString(full[:])}
	} else {
		for off := 0; off < len(buf); off += asarIntegrityBlockSize {
			end := min(off+asarIntegrityBlockSize, len(buf))
			sum := sha256.Sum256(buf[off:end])
			blocks = append(blocks, hex.EncodeToString(sum[:]))
		}
	}
	return &asarIntegrity{
		Algorithm: "SHA256",
		Hash:      hex.EncodeToString(full[:]),
		BlockSize: asarIntegrityBlockSize,
		Blocks:    blocks,
	}
}

func insertNode(root map[string]any, relPath string, val any) {
	parts := strings.Split(strings.Trim(filepath.ToSlash(relPath), "/"), "/")
	cur := root
	for i := 0; i < len(parts)-1; i++ {
		seg := parts[i]
		next, ok := cur[seg].(map[string]any)
		if !ok {
			files := make(map[string]any)
			cur[seg] = map[string]any{"files": files}
			cur = files
			continue
		}
		files, ok := next["files"].(map[string]any)
		if !ok {
			files = make(map[string]any)
			next["files"] = files
		}
		cur = files
	}
	cur[parts[len(parts)-1]] = val
}

func stripBetweenMarkers(content, startMarker, endMarker string) string {
	if startMarker == "" || endMarker == "" {
		return content
	}
	out := content
	for {
		s := strings.Index(out, startMarker)
		if s == -1 {
			break
		}
		e := strings.Index(out[s:], endMarker)
		if e == -1 {
			break
		}
		out = out[:s] + out[s+e+len(endMarker):]
	}
	return out
}

func applyReplacements(content string, pairs [][]any) string {
	out := content
	for _, pair := range pairs {
		if len(pair) < 2 {
			continue
		}
		from, ok1 := pair[0].(string)
		to, ok2 := pair[1].(string)
		if !ok1 || !ok2 || from == "" {
			continue
		}
		all := false
		if len(pair) >= 3 {
			if b, ok := pair[2].(bool); ok {
				all = b
			}
		}
		if all {
			out = strings.ReplaceAll(out, from, to)
		} else {
			out = strings.Replace(out, from, to, 1)
		}
	}
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// ZhPackInfo describes the active zh-pack metadata for the Deep Localization page.
type ZhPackInfo struct {
	AppVersion   string
	EntriesCount int
	UpdatedAt    string
	Source       string
}

// GetZhPackInfo returns metadata for the currently active zh-pack (remote-synced or bundled),
// cached by zh-pack-latest.json stat signature to avoid repeated 440 KB JSON unmarshals on UI ticks.
func GetZhPackInfo(stateDir string) ZhPackInfo {
	source := "KAIKAIKAISER/antigravity-zh-asar"
	sig := stateDir + "|none"
	if stateDir != "" {
		if st, err := os.Stat(filepath.Join(stateDir, "zh-pack-latest.json")); err == nil {
			sig = fmt.Sprintf("%s|%d:%d", stateDir, st.Size(), st.ModTime().UnixNano())
		}
	}
	zhPackMu.Lock()
	if zhInfoCacheSig == sig {
		info := zhInfoCacheVal
		zhPackMu.Unlock()
		return info
	}
	zhPackMu.Unlock()

	pack, err := LoadZhPack(stateDir)
	if err != nil {
		return ZhPackInfo{Source: source}
	}
	cnt := pack.EntriesCount
	if cnt == 0 {
		cnt = len(pack.Entries)
	}
	info := ZhPackInfo{
		AppVersion:   pack.AppVersion,
		EntriesCount: cnt,
		UpdatedAt:    formatPackTime(pack.UpdatedAt),
		Source:       source,
	}
	zhPackMu.Lock()
	zhInfoCacheSig = sig
	zhInfoCacheVal = info
	zhPackMu.Unlock()
	return info
}

func formatPackTime(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, iso)
		if err != nil {
			return iso
		}
	}
	return t.Local().Format("01-02 15:04")
}

// SyncRemoteZhPack downloads and validates zh-pack.json from KAIKAIKAISER/antigravity-zh-asar.
func SyncRemoteZhPack(ctx context.Context, stateDir string) (string, error) {
	if stateDir == "" {
		return "", errors.New("stateDir is empty")
	}
	urls := []string{
		"https://github.com/KAIKAIKAISER/antigravity-zh-asar/releases/latest/download/zh-pack.json",
		"https://raw.githubusercontent.com/KAIKAIKAISER/antigravity-zh-asar/main/zh-pack.json",
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		var pack ZhPackSpec
		if json.Unmarshal(raw, &pack) != nil || pack.Schema != 1 || len(pack.Entries) == 0 || !strings.Contains(pack.RuntimeTemplate, "{{MAP_JSON}}") {
			continue
		}
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(stateDir, "zh-pack-latest.json"), raw, 0o600); err != nil {
			return "", err
		}
		cnt := pack.EntriesCount
		if cnt == 0 {
			cnt = len(pack.Entries)
		}
		return fmt.Sprintf("已同步最新汉化包 (v%s, %d 条)", pack.AppVersion, cnt), nil
	}
	return "", errors.New("无法从 GitHub 拉取最新 zh-pack.json，请检查网络连接")
}

var jsQuotedStringRe = regexp.MustCompile(`"([A-Za-z][A-Za-z0-9 ,.:;!?/'()\-]{2,120})"`)

// ScanResult holds untranslated UI strings discovered in the current Antigravity app.asar.
type ScanResult struct {
	AppVersion  string
	Pending     []string
	ReportPath  string
	TotalFound  int
}

// ScanUntranslatedStrings extracts English UI literals from app.asar (preferring app.asar.bak) and diffs against zh-pack.
func ScanUntranslatedStrings(appDir, stateDir string) (ScanResult, error) {
	st := InspectApp(appDir)
	if !st.AppFound {
		return ScanResult{}, errors.New("未找到 Antigravity app.asar")
	}
	pack, err := LoadZhPack(stateDir)
	if err != nil {
		return ScanResult{}, err
	}
	known := make(map[string]bool, len(pack.Entries)+len(pack.Whitelist))
	for k := range pack.Entries {
		known[strings.ToLower(strings.TrimSpace(k))] = true
	}
	for _, w := range pack.Whitelist {
		known[strings.ToLower(strings.TrimSpace(w))] = true
	}

	scanPath := st.AsarPath
	if st.HasBackup {
		scanPath = st.AsarPath + ".bak"
	}
	arc, err := Open(scanPath)
	if err != nil {
		return ScanResult{}, err
	}
	defer arc.Close()

	var jsFiles []string
	var collect func(files map[string]Entry, prefix string)
	collect = func(files map[string]Entry, prefix string) {
		for name, entry := range files {
			p := name
			if prefix != "" {
				p = prefix + "/" + name
			}
			if entry.Files != nil {
				collect(entry.Files, p)
			} else if strings.HasPrefix(p, "dist/") && strings.HasSuffix(p, ".js") && !strings.Contains(p, "zh_translator_runtime") {
				jsFiles = append(jsFiles, p)
			}
		}
	}
	collect(arc.Header.Files, "")

	seen := map[string]bool{}
	var pending []string
	for _, rel := range jsFiles {
		raw, err := arc.ReadEntry(rel, 6*1024*1024)
		if err != nil {
			continue
		}
		matches := jsQuotedStringRe.FindAllStringSubmatch(string(raw), -1)
		for _, m := range matches {
			s := strings.TrimSpace(m[1])
			low := strings.ToLower(s)
			if known[low] || seen[s] || !strings.Contains(s, " ") {
				continue
			}
			if strings.Contains(s, "http") || strings.Contains(s, "://") || strings.HasPrefix(s, "text-") || strings.HasPrefix(s, "bg-") {
				continue
			}
			seen[s] = true
			pending = append(pending, s)
			if len(pending) >= 300 {
				break
			}
		}
		if len(pending) >= 300 {
			break
		}
	}
	sort.Strings(pending)

	reportPath := ""
	if stateDir != "" {
		reportsDir := filepath.Join(stateDir, "reports")
		_ = os.MkdirAll(reportsDir, 0o700)
		reportPath = filepath.Join(reportsDir, fmt.Sprintf("pending-%s.json", st.AppVersion))
		payload, _ := json.MarshalIndent(map[string]any{
			"appVersion": st.AppVersion,
			"count":      len(pending),
			"pending":    pending,
		}, "", "  ")
		_ = os.WriteFile(reportPath, payload, 0o600)
	}

	return ScanResult{
		AppVersion: st.AppVersion,
		Pending:    pending,
		ReportPath: reportPath,
		TotalFound: len(pending),
	}, nil
}
