package hud

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LsRetryInfo holds parsed retry status from Antigravity's language_server.log tail.
type LsRetryInfo struct {
	Attempt int    `json:"attempt"`
	Reason  string `json:"reason"`
	Delay   string `json:"delay"`
	TsMs    int64  `json:"tsMs"`
}

var (
	lsNoiseRe = regexp.MustCompile(`(?i)play\.googleapis\.com/log|llm_monitor\.go|sir_flash_lite`)
	lsRetryRe = regexp.MustCompile(`(?i)(?:^|\s)[IWE](\d{2})(\d{2})\s+(\d{2}):(\d{2}):(\d{2})\.\d+.*?(?:CallWithRetry|Run):\s*attempt\s*(\d+)\s*failed\s*\((.+)\)(?:,\s*retrying\s+in\s+(\S+))?\s*$`)
)

// GetLsLogSig returns an mtime:size signature for language_server.log.
func GetLsLogSig(lsLogFile string) string {
	if strings.TrimSpace(lsLogFile) == "" {
		return ""
	}
	st, err := os.Stat(lsLogFile)
	if err != nil || st.IsDir() {
		return ""
	}
	return strconv.FormatInt(st.ModTime().UnixMilli(), 10) + ":" + strconv.FormatInt(st.Size(), 10)
}

// ReadRecentLsRetryInfo inspects the last 16KB of language_server.log for recent CallWithRetry / Run retry entries.
func ReadRecentLsRetryInfo(lsLogFile string, sinceMs int64, nowDate time.Time) *LsRetryInfo {
	if strings.TrimSpace(lsLogFile) == "" {
		return nil
	}
	if nowDate.IsZero() {
		nowDate = time.Now()
	}
	st, err := os.Stat(lsLogFile)
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 {
		return nil
	}
	if sinceMs > 0 && st.ModTime().UnixMilli() < sinceMs-2000 {
		return nil
	}

	const maxTail = int64(16384)
	readBytes := st.Size()
	if readBytes > maxTail {
		readBytes = maxTail
	}
	f, err := os.Open(lsLogFile)
	if err != nil {
		return nil
	}
	defer f.Close()

	if _, err := f.Seek(st.Size()-readBytes, io.SeekStart); err != nil {
		return nil
	}
	buf := make([]byte, readBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil
	}
	text := string(buf[:n])
	lines := strings.Split(text, "\n")

	minTs := nowDate.UnixMilli() - 300_000
	if sinceMs > 0 && sinceMs-2000 > minTs {
		minTs = sinceMs - 2000
	}

	loc := nowDate.Location()
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r")
		if line == "" || lsNoiseRe.MatchString(line) {
			continue
		}
		m := lsRetryRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mm, _ := strconv.Atoi(m[1])
		dd, _ := strconv.Atoi(m[2])
		hh, _ := strconv.Atoi(m[3])
		mi, _ := strconv.Atoi(m[4])
		ss, _ := strconv.Atoi(m[5])
		attempt, _ := strconv.Atoi(m[6])
		if attempt < 1 {
			attempt = 1
		}
		dt := time.Date(nowDate.Year(), time.Month(mm), dd, hh, mi, ss, 0, loc)
		if dt.Sub(nowDate) > 30*24*time.Hour {
			dt = time.Date(nowDate.Year()-1, time.Month(mm), dd, hh, mi, ss, 0, loc)
		}
		tsMs := dt.UnixMilli()
		if tsMs < minTs {
			break
		}
		return &LsRetryInfo{
			Attempt: attempt,
			Reason:  strings.TrimSpace(m[7]),
			Delay:   strings.TrimSpace(m[8]),
			TsMs:    tsMs,
		}
	}
	return nil
}
