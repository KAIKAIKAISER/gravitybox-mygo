package hud

import (
	"encoding/hex"
	"math"
	"regexp"
	"strings"
)

var (
	uuidGlobalRe = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	exactUUIDRe  = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	subagentToolNames = map[string]bool{
		"invoke_subagent":             true,
		"default_api:invoke_subagent": true,
		"browser_subagent":            true,
		"start_subagent":              true,
		"spawn_subagent":              true,
	}
)

// ParsedGenMetadata holds fields extracted from gen_metadata.data protobuf blobs.
type ParsedGenMetadata struct {
	StepIndices              []int
	ExecutionID              string
	HasUsage                 bool
	RawInputTokens           int64
	OutputTokens             int64
	CacheWriteTokens         int64
	CachedReadTokens         int64
	ReasoningTokens          int64
	CandidateTokens          int64
	ModelEnum                int64
	ResponseModel            string
	ModelDisplayName         string
	PlannerModelName         string
	MaxContextTokens         int64
	EstimatedTokensUsed      int64
	StartedAtSeconds         float64
	TimeToFirstTokenSeconds  *float64
	StreamingDurationSeconds *float64
}

// ParsedStep represents a decoded conversation step row.
type ParsedStep struct {
	Idx                       int
	StepType                  int
	Status                    int
	CreatedAtSeconds          *float64
	StartedAtSeconds          *float64
	StartTimeSeconds          *float64
	FirstTokenSeconds         *float64
	FinishedGeneratingSeconds *float64
	CompletedAtSeconds        *float64
	EndTimeSeconds            *float64
	RawInputTokens            int64
	OutputTokens              int64
	CacheWriteTokens          int64
	CachedReadTokens          int64
	ReasoningTokens           int64
	CandidateTokens           int64
	ModelMaxTokens            int64
	ModelName                 string
	ToolCallName              string
	IsSubagentStep            bool
	ChildConversationIDs      []string
	ExecutionID               string
	HasUsage                  bool
	Generation                *ParsedGenMetadata
}

func readVarint64(buf []byte, pos *int) (uint64, bool) {
	var val uint64
	var shift uint
	for *pos < len(buf) {
		b := buf[*pos]
		*pos++
		val |= uint64(b&0x7f) << shift
		if (b & 0x80) == 0 {
			return val, true
		}
		shift += 7
		if shift > 63 {
			return 0, false
		}
	}
	return 0, false
}

func skipField(buf []byte, pos *int, wireType int) bool {
	switch wireType {
	case 0:
		_, ok := readVarint64(buf, pos)
		return ok
	case 1:
		if *pos+8 > len(buf) {
			return false
		}
		*pos += 8
		return true
	case 2:
		l, ok := readVarint64(buf, pos)
		if !ok || l > uint64(len(buf)-*pos) {
			return false
		}
		*pos += int(l)
		return true
	case 5:
		if *pos+4 > len(buf) {
			return false
		}
		*pos += 4
		return true
	default:
		return false
	}
}

// ParseProtobufTimestamp decodes google.protobuf.Timestamp / Duration (field 1 sec, field 2 nanos).
func ParseProtobufTimestamp(buf []byte) float64 {
	pos := 0
	var sec int64
	var nanos int64
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 0 {
			v, ok := readVarint64(buf, &pos)
			if !ok {
				break
			}
			if fn == 1 {
				sec = int64(v)
			} else if fn == 2 {
				nanos = int64(int32(v))
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
	res := float64(sec) + float64(nanos)/1e9
	if math.IsNaN(res) || math.IsInf(res, 0) {
		return 0
	}
	return res
}

type usageStats struct {
	rawInput   int64
	output     int64
	cacheWrite int64
	cachedRead int64
	reasoning  int64
	candidate  int64
	modelEnum  int64
}

func parseModelUsageStats(buf []byte) usageStats {
	pos := 0
	var out usageStats
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 0 {
			v, ok := readVarint64(buf, &pos)
			if !ok {
				break
			}
			sv := int64(v)
			if sv < 0 {
				sv = 0
			}
			switch fn {
			case 1:
				out.modelEnum = sv
			case 2:
				out.rawInput = sv
			case 3:
				out.output = sv
			case 4:
				out.cacheWrite = sv
			case 5:
				out.cachedRead = sv
			case 9:
				out.reasoning = sv
			case 10:
				out.candidate = sv
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
	return out
}

func parseModelInfoBlob(buf []byte) (maxTokens int64, modelName string) {
	pos := 0
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 0 {
			v, ok := readVarint64(buf, &pos)
			if !ok {
				break
			}
			if fn == 4 && int64(v) > 0 {
				maxTokens = int64(v)
			}
		} else if wt == 2 {
			l, ok := readVarint64(buf, &pos)
			if !ok || l > uint64(len(buf)-pos) {
				break
			}
			sub := buf[pos : pos+int(l)]
			pos += int(l)
			if (fn == 8 || fn == 12 || fn == 20) && l > 0 && l < 256 {
				s := strings.TrimSpace(string(sub))
				if s != "" && (modelName == "" || fn == 20) {
					modelName = s
				}
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
	return maxTokens, modelName
}

func parseChatToolCallBlob(buf []byte) (name string, childIDs []string) {
	pos := 0
	var candidates [][]byte
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 2 {
			l, ok := readVarint64(buf, &pos)
			if !ok || l > uint64(len(buf)-pos) {
				break
			}
			sub := buf[pos : pos+int(l)]
			pos += int(l)
			if fn == 2 && l > 0 && l < 256 {
				name = strings.TrimSpace(string(sub))
			} else if (fn == 3 || fn == 8) && l > 0 && l < 65536 {
				candidates = append(candidates, sub)
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
	if subagentToolNames[strings.ToLower(name)] {
		seen := make(map[string]bool)
		for _, slice := range candidates {
			for _, m := range uuidGlobalRe.FindAllString(string(slice), -1) {
				low := strings.ToLower(m)
				if !seen[low] {
					seen[low] = true
					childIDs = append(childIDs, low)
				}
			}
		}
	}
	return name, childIDs
}

// ParseStepMetadataBlob decodes a steps row's protobuf blobs into ParsedStep.
func ParseStepMetadataBlob(idx, stepType, status int, metaBlob, dataBlob, renderInfoBlob, payloadBlob []byte) ParsedStep {
	step := ParsedStep{
		Idx:            idx,
		StepType:       stepType,
		Status:         status,
		IsSubagentStep: stepType == 85 || stepType == 127,
	}
	childSet := make(map[string]bool)

	if len(metaBlob) > 0 {
		pos := 0
		var f1Created, f6Viewable, f7Finished, f8Completed, f22LastChunk, f32Started *float64
		for pos < len(metaBlob) {
			tag, ok := readVarint64(metaBlob, &pos)
			if !ok || tag == 0 {
				break
			}
			fn := int(tag >> 3)
			wt := int(tag & 7)
			if wt == 2 {
				l, ok := readVarint64(metaBlob, &pos)
				if !ok || l > uint64(len(metaBlob)-pos) {
					break
				}
				sub := metaBlob[pos : pos+int(l)]
				pos += int(l)

				switch fn {
				case 1:
					if ts := ParseProtobufTimestamp(sub); ts > 0 {
						f1Created = floatPtr(ts)
					}
				case 4:
					tName, ids := parseChatToolCallBlob(sub)
					if tName != "" {
						step.ToolCallName = tName
						if subagentToolNames[strings.ToLower(tName)] {
							step.IsSubagentStep = true
						}
					}
					for _, cid := range ids {
						childSet[cid] = true
					}
				case 6:
					if stepType == 15 {
						if ts := ParseProtobufTimestamp(sub); ts > 0 {
							f6Viewable = floatPtr(ts)
						}
					}
				case 7:
					if ts := ParseProtobufTimestamp(sub); ts > 0 {
						f7Finished = floatPtr(ts)
					}
				case 8:
					if ts := ParseProtobufTimestamp(sub); ts > 0 {
						f8Completed = floatPtr(ts)
					}
				case 9:
					if stepType == 15 {
						u := parseModelUsageStats(sub)
						step.HasUsage = true
						step.RawInputTokens = u.rawInput
						step.OutputTokens = u.output
						step.CacheWriteTokens = u.cacheWrite
						step.CachedReadTokens = u.cachedRead
						step.ReasoningTokens = u.reasoning
						step.CandidateTokens = u.candidate
					}
				case 12:
					step.ExecutionID = hex.EncodeToString(sub)
				case 22:
					if stepType == 15 {
						if ts := ParseProtobufTimestamp(sub); ts > 0 {
							f22LastChunk = floatPtr(ts)
						}
					}
				case 24:
					mt, mn := parseModelInfoBlob(sub)
					if mt > 0 {
						step.ModelMaxTokens = mt
					}
					if mn != "" {
						step.ModelName = mn
					}
				case 32:
					if ts := ParseProtobufTimestamp(sub); ts > 0 {
						f32Started = floatPtr(ts)
					}
				}
			} else if !skipField(metaBlob, &pos, wt) {
				break
			}
		}

		step.CreatedAtSeconds = f1Created
		step.StartedAtSeconds = f32Started
		step.FirstTokenSeconds = f6Viewable
		step.FinishedGeneratingSeconds = f7Finished
		step.CompletedAtSeconds = f8Completed

		if stepType == 15 {
			step.StartTimeSeconds = firstNonNil(f1Created, f32Started)
			step.EndTimeSeconds = firstNonNil(f7Finished, f22LastChunk, f8Completed)
		} else {
			toolEnd := firstNonNil(f8Completed, f7Finished)
			step.EndTimeSeconds = toolEnd
			if f32Started != nil &&
				(f1Created == nil || *f32Started >= *f1Created) &&
				(toolEnd == nil || *f32Started <= *toolEnd) {
				step.StartTimeSeconds = f32Started
			} else {
				step.StartTimeSeconds = firstNonNil(f1Created, f32Started)
			}
		}
	}

	if step.IsSubagentStep || stepType == 132 {
		for _, blob := range [][]byte{dataBlob, renderInfoBlob, payloadBlob} {
			if len(blob) == 0 {
				continue
			}
			limit := len(blob)
			if limit > 65536 {
				limit = 65536
			}
			for _, m := range uuidGlobalRe.FindAllString(string(blob[:limit]), -1) {
				childSet[strings.ToLower(m)] = true
			}
		}
	}

	for cid := range childSet {
		step.ChildConversationIDs = append(step.ChildConversationIDs, cid)
	}
	return step
}

// ParseGenMetadataBlob decodes a row from gen_metadata.data.
func ParseGenMetadataBlob(buf []byte) ParsedGenMetadata {
	var out ParsedGenMetadata
	pos := 0
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt != 2 {
			if !skipField(buf, &pos, wt) {
				break
			}
			continue
		}
		l, ok := readVarint64(buf, &pos)
		if !ok || l > uint64(len(buf)-pos) {
			break
		}
		sub := buf[pos : pos+int(l)]
		pos += int(l)

		switch fn {
		case 1:
			parseChatModelMetadata(sub, &out)
		case 2:
			pPos := 0
			for pPos < len(sub) && len(out.StepIndices) < 10000 {
				v, ok := readVarint64(sub, &pPos)
				if !ok {
					break
				}
				if v <= 0xffffffff {
					out.StepIndices = append(out.StepIndices, int(v))
				}
			}
		case 3:
			parsePlannerConfig(sub, &out)
		case 4:
			out.ExecutionID = hex.EncodeToString(sub)
		}
	}
	return out
}

func parseChatModelMetadata(buf []byte, out *ParsedGenMetadata) {
	pos := 0
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 0 {
			v, ok := readVarint64(buf, &pos)
			if !ok {
				break
			}
			if fn == 3 && v > 0 {
				out.ModelEnum = int64(v)
			}
		} else if wt == 2 {
			l, ok := readVarint64(buf, &pos)
			if !ok || l > uint64(len(buf)-pos) {
				break
			}
			sub := buf[pos : pos+int(l)]
			pos += int(l)
			switch fn {
			case 4:
				u := parseModelUsageStats(sub)
				out.HasUsage = true
				out.RawInputTokens = u.rawInput
				out.OutputTokens = u.output
				out.CacheWriteTokens = u.cacheWrite
				out.CachedReadTokens = u.cachedRead
				out.ReasoningTokens = u.reasoning
				out.CandidateTokens = u.candidate
				if out.ModelEnum == 0 && u.modelEnum > 0 {
					out.ModelEnum = u.modelEnum
				}
			case 9:
				parseChatStartMetadata(sub, out)
			case 11:
				if l <= 32 {
					v := ParseProtobufTimestamp(sub)
					if v >= 0 {
						out.TimeToFirstTokenSeconds = floatPtr(v)
					}
				}
			case 12:
				if l <= 32 {
					v := ParseProtobufTimestamp(sub)
					if v >= 0 {
						out.StreamingDurationSeconds = floatPtr(v)
					}
				}
			case 19:
				if l > 0 && l < 256 {
					out.ResponseModel = strings.TrimSpace(string(sub))
				}
			case 21:
				if l > 0 && l < 256 {
					out.ModelDisplayName = strings.TrimSpace(string(sub))
				}
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
}

func parseChatStartMetadata(buf []byte, out *ParsedGenMetadata) {
	pos := 0
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 2 {
			l, ok := readVarint64(buf, &pos)
			if !ok || l > uint64(len(buf)-pos) {
				break
			}
			sub := buf[pos : pos+int(l)]
			pos += int(l)
			if fn == 4 {
				if ts := ParseProtobufTimestamp(sub); ts >= 946684800 {
					out.StartedAtSeconds = ts
				}
			} else if fn == 10 {
				parseContextWindowMetadata(sub, out)
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
}

func parseContextWindowMetadata(buf []byte, out *ParsedGenMetadata) {
	pos := 0
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 0 {
			v, ok := readVarint64(buf, &pos)
			if !ok {
				break
			}
			if fn == 1 && v > 0 {
				out.EstimatedTokensUsed = int64(v)
			} else if fn == 4 && v > 0 {
				out.MaxContextTokens = int64(v)
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
}

func parsePlannerConfig(buf []byte, out *ParsedGenMetadata) {
	pos := 0
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if wt == 0 {
			v, ok := readVarint64(buf, &pos)
			if !ok {
				break
			}
			if fn == 14 && v > 0 && out.MaxContextTokens == 0 {
				out.MaxContextTokens = int64(v)
			}
		} else if wt == 2 {
			l, ok := readVarint64(buf, &pos)
			if !ok || l > uint64(len(buf)-pos) {
				break
			}
			sub := buf[pos : pos+int(l)]
			pos += int(l)
			if fn == 28 && l > 0 && l < 256 {
				if s := strings.TrimSpace(string(sub)); s != "" {
					out.PlannerModelName = s
				}
			} else if fn == 27 {
				mt, mn := parseModelInfoBlob(sub)
				if mt > 0 && out.MaxContextTokens == 0 {
					out.MaxContextTokens = mt
				}
				if mn != "" && out.PlannerModelName == "" {
					out.PlannerModelName = mn
				}
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
}

// ExtractParentReferences checks trajectory_metadata_blob for parent conversation UUIDs (fields 5, 6).
func ExtractParentReferences(buf []byte) []string {
	pos := 0
	var out []string
	for pos < len(buf) {
		tag, ok := readVarint64(buf, &pos)
		if !ok || tag == 0 {
			break
		}
		fn := int(tag >> 3)
		wt := int(tag & 7)
		if (fn == 5 || fn == 6) && wt == 2 {
			l, ok := readVarint64(buf, &pos)
			if !ok || l > uint64(len(buf)-pos) {
				break
			}
			sub := buf[pos : pos+int(l)]
			pos += int(l)
			if l == 36 && exactUUIDRe.Match(sub) {
				out = append(out, strings.ToLower(string(sub)))
			}
		} else if !skipField(buf, &pos, wt) {
			break
		}
	}
	return out
}

func floatPtr(v float64) *float64 {
	return &v
}

func firstNonNil(ptrs ...*float64) *float64 {
	for _, p := range ptrs {
		if p != nil {
			return p
		}
	}
	return nil
}
