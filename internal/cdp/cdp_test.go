package cdp

import (
	"strings"
	"testing"
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
