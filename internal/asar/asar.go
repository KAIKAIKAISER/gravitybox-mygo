package asar

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxHeaderBytes = 16 * 1024 * 1024

// Entry represents a single file or directory node inside an ASAR header.
type Entry struct {
	Size     json.RawMessage  `json:"size,omitempty"`
	Offset   json.RawMessage  `json:"offset,omitempty"`
	Unpacked bool             `json:"unpacked,omitempty"`
	Link     string           `json:"link,omitempty"`
	Files    map[string]Entry `json:"files,omitempty"`
}

// Header represents the parsed root JSON index of an app.asar archive.
type Header struct {
	Files map[string]Entry `json:"files"`
}

// Archive provides bounded read-only access to an ASAR archive.
type Archive struct {
	file       *os.File
	fileSize   int64
	dataOffset int64
	Header     Header
}

// Status summarizes the Antigravity app.asar and deep localization state.
type Status struct {
	AppFound      bool
	AppDir        string
	ResourcesDir  string
	AsarPath      string
	HasBackup     bool
	AppVersion    string
	IsPatched     bool
	LegacyPatched bool
	TagVersion    string
	TagSource     string
}

// Open parses the 16-byte ASAR preamble and JSON header without loading the whole archive.
func Open(asarPath string) (*Archive, error) {
	f, err := os.Open(asarPath)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size() < 16 {
		_ = f.Close()
		return nil, errors.New("asar header incomplete")
	}

	prefix := make([]byte, 16)
	if _, err := io.ReadFull(f, prefix); err != nil {
		_ = f.Close()
		return nil, err
	}

	headerSize := binary.LittleEndian.Uint32(prefix[4:8])
	jsonSize := binary.LittleEndian.Uint32(prefix[12:16])
	if jsonSize == 0 || jsonSize > maxHeaderBytes {
		_ = f.Close()
		return nil, fmt.Errorf("invalid asar json index size: %d", jsonSize)
	}
	dataOffset := int64(8) + int64(headerSize)
	if int64(16)+int64(jsonSize) > info.Size() || dataOffset > info.Size() {
		_ = f.Close()
		return nil, errors.New("asar header exceeds archive size")
	}

	headerBytes := make([]byte, jsonSize)
	if _, err := io.ReadFull(f, headerBytes); err != nil {
		_ = f.Close()
		return nil, err
	}
	var hdr Header
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Archive{
		file:       f,
		fileSize:   info.Size(),
		dataOffset: dataOffset,
		Header:     hdr,
	}, nil
}

// Close closes the underlying archive file handle.
func (a *Archive) Close() error {
	if a.file == nil {
		return nil
	}
	return a.file.Close()
}

// ReadEntry reads a single bounded file entry (e.g. "package.json" or "dist/preload.js").
func (a *Archive) ReadEntry(relPath string, maxSize int64) ([]byte, error) {
	parts := strings.Split(strings.Trim(relPath, "/"), "/")
	curFiles := a.Header.Files
	var found Entry
	for i, part := range parts {
		node, ok := curFiles[part]
		if !ok {
			return nil, os.ErrNotExist
		}
		if i == len(parts)-1 {
			found = node
			break
		}
		if node.Files == nil {
			return nil, os.ErrNotExist
		}
		curFiles = node.Files
	}
	if found.Unpacked || found.Link != "" || found.Files != nil {
		return nil, errors.New("entry is not a packed regular file")
	}
	size, err := parseNum(found.Size)
	if err != nil {
		return nil, err
	}
	relOffset, err := parseNum(found.Offset)
	if err != nil {
		return nil, err
	}
	if size < 0 || relOffset < 0 || size > maxSize {
		return nil, errors.New("asar entry size out of bounds")
	}
	absOffset := a.dataOffset + relOffset
	if absOffset < a.dataOffset || absOffset+size > a.fileSize {
		return nil, errors.New("asar entry range out of bounds")
	}
	buf := make([]byte, size)
	if _, err := a.file.ReadAt(buf, absOffset); err != nil {
		return nil, err
	}
	return buf, nil
}

func parseNum(raw json.RawMessage) (int64, error) {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, `"`)
	if s == "" {
		return 0, errors.New("empty numeric field")
	}
	return strconv.ParseInt(s, 10, 64)
}

// InspectApp inspects Antigravity's app.asar and localization tag on disk.
func InspectApp(appDir string) Status {
	st := Status{AppDir: appDir}
	if appDir == "" {
		return st
	}
	resDir := filepath.Join(appDir, "resources")
	asarPath := filepath.Join(resDir, "app.asar")
	if _, err := os.Stat(asarPath); err != nil {
		return st
	}
	st.AppFound = true
	st.ResourcesDir = resDir
	st.AsarPath = asarPath
	if _, err := os.Stat(asarPath + ".bak"); err == nil {
		st.HasBackup = true
	}

	arc, err := Open(asarPath)
	if err != nil {
		return st
	}
	defer arc.Close()

	if pkgRaw, err := arc.ReadEntry("package.json", 256*1024); err == nil {
		var pkg struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(pkgRaw, &pkg) == nil {
			st.AppVersion = strings.TrimSpace(pkg.Version)
		}
	}

	if tagRaw, err := arc.ReadEntry("dist/antigravity_zh_cn.tag", 64*1024); err == nil {
		var tag struct {
			Version string `json:"version"`
			Source  string `json:"source"`
		}
		if json.Unmarshal(tagRaw, &tag) == nil {
			st.IsPatched = true
			st.TagVersion = tag.Version
			st.TagSource = tag.Source
		}
	}

	for _, rel := range []string{"dist/preload.js", "dist/main.js", "dist/utils.js"} {
		if raw, err := arc.ReadEntry(rel, 3*1024*1024); err == nil {
			text := string(raw)
			if strings.Contains(text, "injectAntigravityI18n") ||
				strings.Contains(text, "__ANTIGRAVITY_I18N_DATA__") ||
				strings.Contains(text, "createI18nEngine") ||
				strings.Contains(text, "__ag_hanhua_engine__") {
				st.LegacyPatched = true
				break
			}
		}
	}
	return st
}
