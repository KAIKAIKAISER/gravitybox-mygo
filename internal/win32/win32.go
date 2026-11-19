//go:build windows

package win32

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	advapi32                = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW           = advapi32.NewProc("CredReadW")
	procCredWriteW          = advapi32.NewProc("CredWriteW")
	procCredDelete          = advapi32.NewProc("CredDeleteW")
	procCredFree            = advapi32.NewProc("CredFree")
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procPostMessageW        = user32.NewProc("PostMessageW")
)

const (
	swRestore = 9
	wmClose   = 0x0010
)

const (
	credTypeGeneric        = 1
	credPersistLocalSystem = 2
	errNotFound            = syscall.Errno(1168)
)

type winCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// CredRead reads a generic credential directly from Windows Credential Manager (no PowerShell/CGO).
func CredRead(service, account string) (string, bool, error) {
	target, err := windows.UTF16PtrFromString(service + ":" + account)
	if err != nil {
		return "", false, err
	}
	var credPtr *winCredential
	r1, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&credPtr)),
	)
	if r1 == 0 {
		if errors.Is(callErr, errNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("CredReadW failed: %w", callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credPtr)))

	if credPtr.CredentialBlobSize == 0 || credPtr.CredentialBlob == nil {
		return "", true, nil
	}
	buf := unsafe.Slice(credPtr.CredentialBlob, credPtr.CredentialBlobSize)
	return string(bytes.Clone(buf)), true, nil
}

// CredWrite stores a generic credential in Windows Credential Manager.
func CredWrite(service, account, secret string) error {
	target, err := windows.UTF16PtrFromString(service + ":" + account)
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString(account)
	if err != nil {
		return err
	}
	blob := []byte(secret)
	var blobPtr *byte
	if len(blob) > 0 {
		blobPtr = &blob[0]
	}
	cred := winCredential{
		Type:               credTypeGeneric,
		TargetName:         target,
		UserName:           user,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     blobPtr,
		Persist:            credPersistLocalSystem,
	}
	r1, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r1 == 0 {
		return fmt.Errorf("CredWriteW failed: %w", callErr)
	}
	return nil
}

// CredDelete removes a generic credential from Windows Credential Manager.
func CredDelete(service, account string) error {
	target, err := windows.UTF16PtrFromString(service + ":" + account)
	if err != nil {
		return err
	}
	r1, _, callErr := procCredDelete.Call(uintptr(unsafe.Pointer(target)), uintptr(credTypeGeneric), 0)
	if r1 == 0 && !errors.Is(callErr, errNotFound) {
		return fmt.Errorf("CredDeleteW failed: %w", callErr)
	}
	return nil
}

// ProcessEntry represents a running Windows process.
type ProcessEntry struct {
	PID            uint32
	Name           string
	ExecutablePath string
}

// ListProcesses enumerates active Windows processes via Toolhelp32 (no PowerShell).
func ListProcesses() ([]ProcessEntry, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil, err
	}

	var out []ProcessEntry
	for {
		name := windows.UTF16ToString(pe.ExeFile[:])
		exePath := queryProcessImagePath(pe.ProcessID)
		out = append(out, ProcessEntry{
			PID:            pe.ProcessID,
			Name:           name,
			ExecutablePath: exePath,
		})
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return out, nil
}

func queryProcessImagePath(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH*2)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// IsAntigravityRunning checks if Antigravity.exe is active.
func IsAntigravityRunning() bool {
	procs, err := ListProcesses()
	if err != nil {
		return false
	}
	for _, p := range procs {
		if strings.EqualFold(p.Name, "Antigravity.exe") ||
			strings.EqualFold(filepath.Base(p.ExecutablePath), "Antigravity.exe") {
			return true
		}
	}
	return false
}

// ListeningPort represents a local TCP LISTENING socket.
type ListeningPort struct {
	PID     uint32
	Address string
	Port    int
}

// ListListeningPorts queries active local TCP LISTENING sockets using netstat.
func ListListeningPorts(ctx context.Context) ([]ListeningPort, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "netstat.exe", "-ano", "-p", "TCP")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var results []ListeningPort
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		if !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		idx := strings.LastIndex(fields[1], ":")
		if idx <= 0 {
			continue
		}
		port, err := strconv.Atoi(fields[1][idx+1:])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		pid64, err := strconv.ParseUint(fields[4], 10, 32)
		if err != nil || pid64 == 0 {
			continue
		}
		results = append(results, ListeningPort{
			PID:     uint32(pid64),
			Address: fields[1],
			Port:    port,
		})
	}
	return results, nil
}

// LaunchAntigravity starts Antigravity.exe cleanly as a visible GUI application with remote debugging enabled.
func LaunchAntigravity(exePath string, debugPort int) error {
	if debugPort <= 0 {
		debugPort = 9222
	}
	cmd := exec.Command(exePath, fmt.Sprintf("--remote-debugging-port=%d", debugPort), "--remote-allow-origins=*")
	cmd.Dir = filepath.Dir(exePath)
	return cmd.Start()
}

// OpenAntigravity brings an already-running Antigravity window to the foreground, or launches Antigravity.exe if not running.
func OpenAntigravity(exePath string, debugPort int) error {
	if FocusRunningAntigravity() {
		return nil
	}
	if exePath == "" {
		return errors.New("未找到 Antigravity.exe")
	}
	return LaunchAntigravity(exePath, debugPort)
}

func antigravityPIDSet() map[uint32]bool {
	procs, err := ListProcesses()
	if err != nil {
		return nil
	}
	pids := make(map[uint32]bool)
	for _, p := range procs {
		if strings.EqualFold(p.Name, "Antigravity.exe") ||
			strings.EqualFold(filepath.Base(p.ExecutablePath), "Antigravity.exe") {
			pids[p.PID] = true
		}
	}
	return pids
}

// FocusRunningAntigravity restores and focuses any visible top-level Antigravity window.
func FocusRunningAntigravity() bool {
	pids := antigravityPIDSet()
	if len(pids) == 0 {
		return false
	}
	var activated bool
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var pid uint32
		_, _, _ = procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if !pids[pid] {
			return 1
		}
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		_, _, _ = procShowWindow.Call(hwnd, swRestore)
		_, _, _ = procSetForegroundWindow.Call(hwnd)
		activated = true
		return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0)
	return activated
}

// LanguageServerProc describes a running Antigravity language_server process.
type LanguageServerProc struct {
	PID         uint32
	CommandLine string
}

// FindLanguageServerProcesses queries running language_server* processes and their command lines.
func FindLanguageServerProcesses(ctx context.Context) ([]LanguageServerProc, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	ps := `[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; Get-CimInstance Win32_Process -Filter "Name like 'language_server%'" | ForEach-Object { "$($_.ProcessId)|$($_.CommandLine)" }`
	cmd := exec.CommandContext(cctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var res []LanguageServerProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sep := strings.IndexByte(line, '|')
		if sep <= 0 {
			continue
		}
		pid64, err := strconv.ParseUint(strings.TrimSpace(line[:sep]), 10, 32)
		if err != nil || pid64 == 0 {
			continue
		}
		res = append(res, LanguageServerProc{
			PID:         uint32(pid64),
			CommandLine: strings.TrimSpace(line[sep+1:]),
		})
	}
	return res, nil
}

// StopAntigravityGracefully asks Antigravity.exe windows to close via WM_CLOSE before cleaning lingering child trees.
func StopAntigravityGracefully(ctx context.Context) error {
	pids := antigravityPIDSet()
	if len(pids) == 0 {
		return nil
	}
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var pid uint32
		_, _, _ = procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if !pids[pid] {
			return 1
		}
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis != 0 {
			_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}
		return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0)

	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !IsAntigravityRunning() {
			return nil
		}
		time.Sleep(120 * time.Millisecond)
	}

	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	fcmd := exec.CommandContext(cctx, "taskkill.exe", "/IM", "Antigravity.exe", "/T", "/F")
	fcmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = fcmd.Run()

	cleanDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(cleanDeadline) {
		if !IsAntigravityRunning() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

const runRegPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetLaunchAtLogin registers or removes GravityBox from HKCU Run startup.
func SetLaunchAtLogin(enabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runRegPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if !enabled {
		err := k.DeleteValue("GravityBox")
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("GravityBox", fmt.Sprintf(`"%s"`, exe))
}

// OpenInExplorer opens a folder in Windows Explorer.
func OpenInExplorer(path string) error {
	cmd := exec.Command("explorer.exe", path)
	return cmd.Start()
}

// OpenExternalURL opens an https:// URL in the default Windows browser.
func OpenExternalURL(rawURL string) error {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return errors.New("unsupported URL scheme")
	}
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", rawURL)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
