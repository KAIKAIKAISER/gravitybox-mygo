package asar

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTestAsar(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	root := map[string]any{}
	var payload []byte
	var offset int64
	for name, content := range files {
		sz := int64(len(content))
		insertNode(root, name, map[string]any{
			"size":   sz,
			"offset": offset,
		})
		payload = append(payload, content...)
		offset += sz
	}
	jsonBuf, err := json.Marshal(map[string]any{"files": root})
	if err != nil {
		t.Fatal(err)
	}
	aligned := ((4 + len(jsonBuf) + 3) / 4) * 4
	headerSize := 4 + aligned
	pad := aligned - 4 - len(jsonBuf)

	var prefix [16]byte
	binary.LittleEndian.PutUint32(prefix[0:4], 4)
	binary.LittleEndian.PutUint32(prefix[4:8], uint32(headerSize))
	binary.LittleEndian.PutUint32(prefix[8:12], uint32(aligned))
	binary.LittleEndian.PutUint32(prefix[12:16], uint32(len(jsonBuf)))

	var out []byte
	out = append(out, prefix[:]...)
	out = append(out, jsonBuf...)
	out = append(out, make([]byte, pad)...)
	out = append(out, payload...)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAndPatchAsarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.asar")
	dst := filepath.Join(dir, "patched.asar")

	writeTestAsar(t, src, map[string][]byte{
		"package.json":    []byte(`{"name":"antigravity","version":"2.32.2"}`),
		"dist/preload.js": []byte(`console.log("preload");`),
	})

	arc, err := Open(src)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	pkg, err := arc.ReadEntry("package.json", 4096)
	if err != nil || string(pkg) != `{"name":"antigravity","version":"2.32.2"}` {
		t.Fatalf("ReadEntry package.json mismatch: %q, err=%v", string(pkg), err)
	}
	_ = arc.Close()

	overrides := map[string][]byte{
		"dist/preload.js":          []byte(`console.log("patched");`),
		"dist/antigravity_zh_cn.tag": []byte(`{"version":"2.32.2"}`),
	}
	if err := patchArchive(src, dst, overrides); err != nil {
		t.Fatalf("patchArchive failed: %v", err)
	}

	arc2, err := Open(dst)
	if err != nil {
		t.Fatalf("Open patched failed: %v", err)
	}
	defer arc2.Close()

	gotPreload, err := arc2.ReadEntry("dist/preload.js", 4096)
	if err != nil || string(gotPreload) != `console.log("patched");` {
		t.Fatalf("patched preload mismatch: %q, err=%v", string(gotPreload), err)
	}
	gotTag, err := arc2.ReadEntry("dist/antigravity_zh_cn.tag", 4096)
	if err != nil || string(gotTag) != `{"version":"2.32.2"}` {
		t.Fatalf("tag mismatch: %q, err=%v", string(gotTag), err)
	}
}
