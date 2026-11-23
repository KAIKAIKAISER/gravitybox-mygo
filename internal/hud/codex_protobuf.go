package hud

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ProtoFields stores decoded wire protobuf field numbers (varints and byte slices).
type ProtoFields struct {
	Varints map[int]int64
	Bytes   map[int][]byte
}

func (f ProtoFields) HasVarint(fn int) bool {
	_, ok := f.Varints[fn]
	return ok
}

func (f ProtoFields) HasBytes(fn int) bool {
	_, ok := f.Bytes[fn]
	return ok
}

func (f ProtoFields) Varint(fn int) int64 {
	return f.Varints[fn]
}

func (f ProtoFields) BytesVal(fn int) []byte {
	return f.Bytes[fn]
}

// ParseProto decodes top-level protobuf wire fields identically to core/daemon/protobuf.mjs.
func ParseProto(buf []byte) ProtoFields {
	out := ProtoFields{
		Varints: make(map[int]int64),
		Bytes:   make(map[int][]byte),
	}
	i := 0
	for i < len(buf) {
		tag, ok := readVarint64(buf, &i)
		if !ok {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if fn <= 0 || fn > 10000 {
			break
		}
		switch wt {
		case 0:
			val, ok := readVarint64(buf, &i)
			if !ok {
				return out
			}
			out.Varints[fn] = int64(val)
		case 2:
			length, ok := readVarint64(buf, &i)
			if !ok || length > uint64(len(buf)-i) {
				return out
			}
			out.Bytes[fn] = buf[i : i+int(length)]
			i += int(length)
		case 1:
			if i+8 > len(buf) {
				return out
			}
			out.Bytes[fn] = buf[i : i+8]
			i += 8
		case 5:
			if i+4 > len(buf) {
				return out
			}
			out.Bytes[fn] = buf[i : i+4]
			i += 4
		default:
			return out
		}
	}
	return out
}

// ExtractMaxStepIdx extracts the maximum step index from either a scalar varint or packed repeated varint buffer.
func ExtractMaxStepIdx(top ProtoFields) int {
	if v, ok := top.Varints[2]; ok {
		if v >= 0 {
			return int(v)
		}
		return -1
	}
	raw, ok := top.Bytes[2]
	if !ok || len(raw) == 0 {
		return -1
	}
	i := 0
	maxVal := -1
	for i < len(raw) {
		val, ok := readVarint64(raw, &i)
		if !ok {
			break
		}
		if int(val) > maxVal {
			maxVal = int(val)
		}
	}
	return maxVal
}

// CodexGenItem mirrors parseGenMetadataRow output from antigravity-codex-theme.
type CodexGenItem struct {
	Idx        int
	InTok      int64
	OutTok     int64
	CacheWrite int64
	CacheRead  int64
	Ctx        int64
	MaxCtx     int64
	MaxStepIdx int
	ModelName  string
	ThinkTok   int64
	RespTok    int64
	TTFT       float64
	Stream     float64
	TotDur     float64
	ErrCode    int64
	ErrMsg     string
}

// ParseCodexGenRow parses a single gen_metadata row using Codex protobuf semantics.
func ParseCodexGenRow(idx int, data []byte) CodexGenItem {
	top := ParseProto(data)
	chat := ParseProto(top.Bytes[1])
	usage := ParseProto(chat.Bytes[4])
	chatStart := ParseProto(chat.Bytes[9])
	ctxMeta := ParseProto(chatStart.Bytes[10])
	ttftMap := ParseProto(chat.Bytes[11])
	streamMap := ParseProto(chat.Bytes[12])
	planner := ParseProto(top.Bytes[3])
	meta17 := ParseProto(chat.Bytes[17])

	inTok := clampNonNeg64(usage.Varint(2))
	outTok := clampNonNeg64(usage.Varint(3))
	cacheWrite := clampNonNeg64(usage.Varint(4))
	cacheRead := clampNonNeg64(usage.Varint(5))
	maxCtx := clampNonNeg64(ctxMeta.Varint(4))
	maxStepIdx := ExtractMaxStepIdx(top)
	thinkTok := clampNonNeg64(usage.Varint(9))
	respTok := clampNonNeg64(usage.Varint(10))
	ttft := float64(ttftMap.Varint(1)) + float64(ttftMap.Varint(2))/1e9
	stream := float64(streamMap.Varint(1)) + float64(streamMap.Varint(2))/1e9

	modelName := strings.TrimSpace(string(chat.Bytes[21]))
	if modelName == "" {
		modelName = strings.TrimSpace(string(chat.Bytes[19]))
	}
	if modelName == "" {
		modelName = strings.TrimSpace(string(planner.Bytes[28]))
	}

	errCode := meta17.Varint(5)
	errMsg := strings.TrimSpace(string(meta17.Bytes[3]))

	return CodexGenItem{
		Idx:        idx,
		InTok:      inTok,
		OutTok:     outTok,
		CacheWrite: cacheWrite,
		CacheRead:  cacheRead,
		Ctx:        inTok + cacheWrite + cacheRead,
		MaxCtx:     maxCtx,
		MaxStepIdx: maxStepIdx,
		ModelName:  modelName,
		ThinkTok:   thinkTok,
		RespTok:    respTok,
		TTFT:       ttft,
		Stream:     stream,
		TotDur:     ttft + stream,
		ErrCode:    errCode,
		ErrMsg:     errMsg,
	}
}

// CheckpointStepInfo represents parsed info from a step_type=23 checkpoint row.
type CheckpointStepInfo struct {
	Idx          int
	Status       int
	CpMetaIdx    int
	SummaryBytes int
}

// ParseCheckpointStepInfo extracts checkpoint step metadata & summary size.
func ParseCheckpointStepInfo(idx, status int, metadata, stepPayload []byte) *CheckpointStepInfo {
	cpMetaIdx := -1
	if len(metadata) > 0 {
		meta := ParseProto(metadata)
		if b, ok := meta.Bytes[20]; ok {
			stepInfo := ParseProto(b)
			if v, ok := stepInfo.Varints[3]; ok {
				cpMetaIdx = int(v)
			}
		}
	}
	summaryBytes := 0
	if len(stepPayload) > 0 {
		top := ParseProto(stepPayload)
		if b, ok := top.Bytes[30]; ok {
			cpBody := ParseProto(b)
			if sb, ok := cpBody.Bytes[5]; ok {
				summaryBytes = len(sb)
			}
		}
	}
	return &CheckpointStepInfo{
		Idx:          idx,
		Status:       status,
		CpMetaIdx:    cpMetaIdx,
		SummaryBytes: summaryBytes,
	}
}

// ExtractStepTimestampParts returns (sec, nanos) from step metadata field 1.
func ExtractStepTimestampParts(metaBuf []byte) (int64, int64) {
	if len(metaBuf) == 0 {
		return 0, 0
	}
	meta := ParseProto(metaBuf)
	tsBuf, ok := meta.Bytes[1]
	if !ok {
		return 0, 0
	}
	ts := ParseProto(tsBuf)
	return ts.Varint(1), ts.Varint(2)
}

// ExtractStepStartMs returns millisecond timestamp from step metadata.
func ExtractStepStartMs(metaBuf []byte) int64 {
	sec, nanos := ExtractStepTimestampParts(metaBuf)
	if sec <= 0 {
		return 0
	}
	return sec*1000 + nanos/1_000_000
}

// ExtractUserStepSig builds a lightweight signature for detecting regenerated user steps.
func ExtractUserStepSig(stepIdx int, metaBuf []byte, payloadLen int) string {
	if stepIdx < 0 {
		return ""
	}
	sec, nanos := ExtractStepTimestampParts(metaBuf)
	return strconv.Itoa(stepIdx) + ":" + strconv.FormatInt(sec, 10) + "." + strconv.FormatInt(nanos, 10) + ":" + strconv.Itoa(payloadLen)
}

var (
	cancelTextRe       = regexp.MustCompile(`(?i)(\bcontext\s+canceled\b|\bcancell?ed\s+by\s+user\b|\baborted\s+by\s+user\b|\buser\s+cancell?ed\b)`)
	trailingUUIDRe     = regexp.MustCompile(`(?i)^[0-9a-f-]{32,}(?:-\d+)*$`)
	apiErrHintRe       = regexp.MustCompile(`(?i)(?:API error|request failed|stream|timeout|EOF|tls:|bad record|unavailable|503|429|ECONN|canceled|cancelled)`)
	apiErrCleanHintRe  = regexp.MustCompile(`(?i)(?:API error|request failed|stream|timeout|EOF|tls:|bad record|unavailable|503|429|ECONN)`)
)

// IsCancelText checks whether an error string represents deliberate user cancellation.
func IsCancelText(msg string) bool {
	return cancelTextRe.MatchString(strings.TrimSpace(msg))
}

func hasControlChars(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b <= 0x08) || b == 0x0b || b == 0x0c || (b >= 0x0e && b <= 0x1f) || b == 0x7f {
			return true
		}
	}
	return false
}

// ExtractProtoLeafStrings recursively decodes clean UTF-8 leaf strings before descending.
func ExtractProtoLeafStrings(buf []byte, depth int, out *[]string) {
	if len(buf) == 0 || depth > 5 {
		return
	}
	if len(buf) >= 6 && utf8.Valid(buf) {
		s := strings.TrimSpace(string(buf))
		if s != "" && !hasControlChars(s) && !trailingUUIDRe.MatchString(s) {
			*out = append(*out, s)
			return
		}
	}
	// Preserve field order when descending wire bytes.
	i := 0
	for i < len(buf) {
		tag, ok := readVarint64(buf, &i)
		if !ok {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if fn <= 0 || fn > 10000 {
			break
		}
		switch wt {
		case 0:
			if _, ok := readVarint64(buf, &i); !ok {
				return
			}
		case 2:
			length, ok := readVarint64(buf, &i)
			if !ok || length > uint64(len(buf)-i) {
				return
			}
			sub := buf[i : i+int(length)]
			i += int(length)
			ExtractProtoLeafStrings(sub, depth+1, out)
		case 1:
			if i+8 > len(buf) {
				return
			}
			i += 8
		case 5:
			if i+4 > len(buf) {
				return
			}
			i += 4
		default:
			return
		}
	}
}

// ExtractStep17Message extracts an error summary from step_type=17 step_payload/error_details.
func ExtractStep17Message(stepPayload, errorDetails []byte) string {
	var protoStrings []string
	if len(stepPayload) > 0 {
		ExtractProtoLeafStrings(stepPayload, 0, &protoStrings)
	}
	if len(errorDetails) > 0 {
		ExtractProtoLeafStrings(errorDetails, 0, &protoStrings)
	}
	for i := len(protoStrings) - 1; i >= 0; i-- {
		if apiErrHintRe.MatchString(protoStrings[i]) {
			return truncateRunes(protoStrings[i], 200)
		}
	}
	if len(protoStrings) > 0 {
		return truncateRunes(protoStrings[len(protoStrings)-1], 200)
	}

	var chunks []string
	if len(stepPayload) > 0 {
		chunks = append(chunks, string(stepPayload))
	}
	if len(errorDetails) > 0 {
		chunks = append(chunks, string(errorDetails))
	}
	cleanedRaw := stripNonPrintableToNewlines(strings.Join(chunks, "\n"))
	var cleaned []string
	for _, line := range strings.Split(cleanedRaw, "\n") {
		s := strings.TrimSpace(line)
		if len(s) >= 6 && !trailingUUIDRe.MatchString(s) {
			cleaned = append(cleaned, s)
		}
	}
	for i := len(cleaned) - 1; i >= 0; i-- {
		if apiErrCleanHintRe.MatchString(cleaned[i]) {
			return truncateRunes(cleaned[i], 200)
		}
	}
	if len(cleaned) > 0 {
		return truncateRunes(cleaned[len(cleaned)-1], 200)
	}
	return ""
}

func sanitizeAsciiCjkSpaces(raw string) string {
	var sb strings.Builder
	prevSpace := true
	for _, r := range raw {
		if (r >= 0x20 && r <= 0x7e) || (r >= 0x4e00 && r <= 0x9fff) {
			sb.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			sb.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(sb.String())
}

func stripNonPrintableToNewlines(raw string) string {
	var sb strings.Builder
	for _, r := range raw {
		if r == '\n' || (r >= 0x20 && r <= 0x7e) || (r >= 0x4e00 && r <= 0x9fff) {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func truncateRunes(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars])
}

func clampNonNeg64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
