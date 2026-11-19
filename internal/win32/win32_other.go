//go:build !windows

package win32

import (
	"context"
	"errors"
)

type ProcessEntry struct {
	PID            uint32
	Name           string
	ExecutablePath string
}

type ListeningPort struct {
	PID     uint32
	Address string
	Port    int
}

type LanguageServerProc struct {
	PID         uint32
	CommandLine string
}

func CredRead(service, account string) (string, bool, error) {
	return "", false, errors.New("win32 credentials only supported on Windows")
}

func CredWrite(service, account, secret string) error {
	return errors.New("win32 credentials only supported on Windows")
}

func CredDelete(service, account string) error {
	return errors.New("win32 credentials only supported on Windows")
}

func ListProcesses() ([]ProcessEntry, error) { return nil, nil }
func IsAntigravityRunning() bool             { return false }
func ListListeningPorts(context.Context) ([]ListeningPort, error) {
	return nil, nil
}
func LaunchAntigravity(string, int) error {
	return errors.New("LaunchAntigravity only supported on Windows")
}
func OpenAntigravity(string, int) error {
	return errors.New("OpenAntigravity only supported on Windows")
}
func FocusRunningAntigravity() bool { return false }
func FindLanguageServerProcesses(context.Context) ([]LanguageServerProc, error) {
	return nil, nil
}
func StopAntigravityGracefully(context.Context) error {
	return errors.New("StopAntigravityGracefully only supported on Windows")
}
func SetLaunchAtLogin(bool) error { return nil }
func OpenInExplorer(string) error { return nil }
func OpenExternalURL(string) error { return nil }
