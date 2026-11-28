package cdp

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAssertLoopback(t *testing.T) {
	if _, err := AssertLoopback("ws://127.0.0.1:9222/devtools/page/1"); err != nil {
		t.Fatalf("expected loopback ws to succeed: %v", err)
	}
	if _, err := AssertLoopback("http://example.com/json"); err == nil {
		t.Fatal("expected remote host to be rejected")
	}
	if _, err := AssertLoopback("ws://user:pass@127.0.0.1:9222/devtools"); err == nil {
		t.Fatal("expected credentials in URL to be rejected")
	}
}

func TestIsAllowedTarget(t *testing.T) {
	if !isAllowedTarget(Target{
		Type:                 "page",
		URL:                  "http://127.0.0.1:3000/c/11111111-1111-1111-1111-111111111111",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/1",
	}) {
		t.Fatal("expected local Antigravity conversation target to be allowed")
	}
	if isAllowedTarget(Target{
		Type:                 "page",
		URL:                  "https://accounts.google.com/signin",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/2",
	}) {
		t.Fatal("expected external auth URL to be rejected")
	}
}

func TestBuildRuntimeBundle(t *testing.T) {
	bundle, err := buildRuntimeBundle()
	if err != nil {
		t.Fatalf("buildRuntimeBundle failed: %v", err)
	}
	if !strings.Contains(bundle, "bootstrapGravityBoxHud") {
		t.Fatal("expected HUD bootstrap wrapper in runtime bundle")
	}
	if strings.Contains(bundle, "\nimport ") {
		t.Fatal("expected ESM imports to be stripped from classic runtime bundle")
	}
}

func TestNewControllerLoadsDictionary(t *testing.T) {
	ctrl, err := NewController(9222, "zh-Hans", true, true)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}
	defer ctrl.Stop()
	st := ctrl.Status()
	if st.DictionaryEntries < 1000 {
		t.Fatalf("expected >1000 dictionary entries, got %d", st.DictionaryEntries)
	}
	if st.DictionaryVersion == "base" {
		t.Fatalf("expected user zh-pack dictionary version instead of legacy base")
	}
}

func TestDialWSOmitsOriginHeader(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	originSeen := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		originSeen <- req.Header.Get("Origin")
		key := req.Header.Get("Sec-WebSocket-Key")
		sum := sha1.Sum([]byte(key + wsGUID))
		accept := base64.StdEncoding.EncodeToString(sum[:])
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"))
		time.Sleep(100 * time.Millisecond)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, err := dialWS(ctx, "ws://"+ln.Addr().String()+"/devtools/page/test")
	if err != nil {
		t.Fatalf("dialWS failed: %v", err)
	}
	defer ws.Close()

	if got := <-originSeen; got != "" {
		t.Fatalf("expected empty Origin header to satisfy Chromium DevTools check, got %q", got)
	}
}

func TestBuildCodexBundleAndSwarmTarget(t *testing.T) {
	if !isAllowedTarget(Target{
		Type:                 "iframe",
		URL:                  "plugin://swarm--local/view",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/swarm-1",
	}) {
		t.Fatal("expected swarm plugin iframe to be permitted")
	}

	ctrl, err := NewController(9222, "zh-Hans", true, true)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}
	defer ctrl.Stop()

	ctrl.UpdateCodexSettings(CodexSettings{
		ThemeEnabled:       true,
		ThemePreset:        "codex-dark",
		TelemetryLayout:    "detailed",
		TelemetryCacheMode: "step",
		ThemeVars:          map[string]string{"--primary": "#059669"},
	})

	ctrl.mu.Lock()
	ver := ctrl.codexPatchVersion
	cfg := ctrl.codexCfg
	ctrl.mu.Unlock()

	expr, err := ctrl.buildCodexMainBootExpression(false, true, true, cfg, ver)
	if err != nil {
		t.Fatalf("buildCodexMainBootExpression failed: %v", err)
	}
	if !strings.Contains(expr, `"themeForceMode":"dark"`) || !strings.Contains(expr, `"telemetryLayout":"detailed"`) || !strings.Contains(expr, `computeStatusSnapshot`) {
		t.Fatalf("unexpected codex main boot expression")
	}
	if strings.Contains(expr, `"customFont":`) || strings.Contains(expr, `"i18n":`) {
		t.Fatalf("expected customFont and i18n module flags to be removed")
	}

	// Ensure no system font stacks or custom font-override selectors remain anywhere in codexFullCSS.
	for _, needle := range []string{"system-ui", "Segoe UI", "Consolas", "data-ag-custom-font", "--vscode-font-family", "--ag-chat-font-size", "letter-spacing: -0.0"} {
		if strings.Contains(ctrl.codexFullCSS, needle) {
			t.Fatalf("found forbidden font override %q in codexFullCSS", needle)
		}
	}
}
