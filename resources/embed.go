package resources

import (
	"embed"
	"io/fs"
)

//go:embed icon.png assets/* assets/i18n/* assets/model-icons/* dict/* runtime/*
var Embedded embed.FS

// ReadFile reads an embedded resource file using forward-slash relative paths (e.g. "dict/base.json").
func ReadFile(rel string) ([]byte, error) {
	return fs.ReadFile(Embedded, rel)
}
