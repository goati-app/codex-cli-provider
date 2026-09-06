package codexcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ProtocolResult is the bounded, provider-neutral interpretation of a Codex
// JSONL event stream. Most consumers should use Executor.Run instead.
type ProtocolResult struct {
	Raw                    string
	OfficialWebSearchCalls int
	InferredSearchQueries  int
	FinalMessageEvent      int
	LastWebSearchEvent     int
	Events                 int
	ForbiddenEvents        int
	InputTokens            int
	OutputTokens           int
	CachedInputTokens      *int
	ReasoningOutputTokens  *int
	TokensReported         bool
	Model                  string
	ThreadID               string
}

// ParseJSONL parses an already-bounded event stream. It returns fields observed
// before an invalid event together with the error so partial usage is retained.
func ParseJSONL(data []byte, maxJSONL, maxResult int64, maxMetadata int) (ProtocolResult, error) {
	if maxJSONL <= 0 {
		maxJSONL = DefaultJSONLMaxBytes
	}
	if maxResult <= 0 {
		maxResult = DefaultResultMaxBytes
	}
	if maxMetadata <= 0 {
		maxMetadata = DefaultMetadataMaxBytes
	}
	if maxJSONL > DefaultJSONLMaxBytes || maxResult > DefaultResultMaxBytes || maxMetadata > DefaultMetadataMaxBytes {
		return ProtocolResult{}, newError(InvalidConfig, "configure protocol limits", fmt.Errorf("limit exceeds safety ceiling"))
	}
	if int64(len(data)) > maxJSONL {
		return ProtocolResult{}, newError(ProtocolInvalid, "parse", fmt.Errorf("JSONL exceeds limit"))
	}
	result := ProtocolResult{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), int(maxJSONL)+1)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event any
		if err := json.Unmarshal(line, &event); err != nil {
			return result, newError(ProtocolInvalid, "parse", err)
		}
		result.Events++
		walkEvent(event, &result)
	}
	if err := scanner.Err(); err != nil {
		return result, newError(ProtocolInvalid, "parse", err)
	}
	if result.ForbiddenEvents > 0 {
		return result, newError(ProtocolInvalid, "parse", fmt.Errorf("forbidden local tool event"))
	}
	if result.Events == 0 {
		return result, newError(ProtocolInvalid, "parse", fmt.Errorf("no JSONL events"))
	}
	if len([]byte(result.ThreadID)) > maxMetadata {
		result.ThreadID = ""
		return result, newError(ProtocolInvalid, "parse", fmt.Errorf("thread id exceeds limit"))
	}
	if len([]byte(result.Model)) > maxMetadata {
		result.Model = ""
		return result, newError(ProtocolInvalid, "parse", fmt.Errorf("model id exceeds limit"))
	}
	if strings.TrimSpace(result.Raw) == "" {
		return result, newError(SchemaInvalid, "parse", fmt.Errorf("no final agent message"))
	}
	if int64(len([]byte(result.Raw))) > maxResult {
		result.Raw = ""
		return result, newError(ProtocolInvalid, "parse", fmt.Errorf("final result exceeds limit"))
	}
	return result, nil
}

func walkEvent(value any, result *ProtocolResult) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			walkEvent(item, result)
		}
	case map[string]any:
		if v, ok := typed["thread_id"].(string); ok && strings.TrimSpace(v) != "" {
			result.ThreadID = strings.TrimSpace(v)
		}
		if v, ok := typed["model"].(string); ok && strings.TrimSpace(v) != "" {
			result.Model = strings.TrimSpace(v)
		}
		if usage, ok := typed["usage"].(map[string]any); ok {
			input, inputOK := nonNegativeInt(usage["input_tokens"])
			output, outputOK := nonNegativeInt(usage["output_tokens"])
			if inputOK && outputOK {
				result.InputTokens, result.OutputTokens, result.TokensReported = input, output, true
			}
			if v, ok := nonNegativeInt(usage["cached_input_tokens"]); ok {
				result.CachedInputTokens = &v
			}
			if v, ok := nonNegativeInt(usage["reasoning_output_tokens"]); ok {
				result.ReasoningOutputTokens = &v
			}
		}
		if eventType, ok := typed["type"].(string); ok && forbiddenEventType(eventType) {
			result.ForbiddenEvents++
		}
		if action, ok := typed["action"].(map[string]any); ok {
			if actionType, ok := action["type"].(string); ok && forbiddenEventType(actionType) {
				result.ForbiddenEvents++
			}
			if actionType, _ := action["type"].(string); actionType == "search" {
				result.OfficialWebSearchCalls++
				result.LastWebSearchEvent = result.Events
				if queries, ok := action["queries"].([]any); ok {
					result.InferredSearchQueries += len(queries)
				} else if query, ok := action["query"].(string); ok && strings.TrimSpace(query) != "" {
					result.InferredSearchQueries++
				}
			}
		}
		eventType, _ := typed["type"].(string)
		if text, ok := finalText(eventType, typed); ok {
			result.Raw, result.FinalMessageEvent = text, result.Events
		}
		for key, child := range typed {
			if key != "text" && key != "content" {
				walkEvent(child, result)
			}
		}
	}
}

func ValidateWebSearchResult(result ProtocolResult, maxCalls int) error {
	if result.OfficialWebSearchCalls == 0 {
		return newError(ProtocolInvalid, "validate web search", fmt.Errorf("no official web search action"))
	}
	if maxCalls > 0 && result.OfficialWebSearchCalls > maxCalls {
		return newError(ProtocolInvalid, "validate web search", fmt.Errorf("exceeded maximum of %d official web search actions", maxCalls))
	}
	if result.FinalMessageEvent <= result.LastWebSearchEvent {
		return newError(SchemaInvalid, "validate web search", fmt.Errorf("no final agent message after the last web search action"))
	}
	return nil
}

func nonNegativeInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		value, err := typed.Int64()
		return int(value), err == nil && value >= 0
	default:
		return 0, false
	}
}

func forbiddenEventType(raw string) bool {
	typeName := strings.ToLower(strings.TrimSpace(raw))
	if typeName == "" || typeName == "search" || typeName == "web_search" || typeName == "web_search_call" {
		return false
	}
	for _, token := range []string{"command", "shell", "unified_exec", "file", "mcp", "browser", "computer", "image_generation", "plugin", "skill"} {
		if strings.Contains(typeName, token) {
			return true
		}
	}
	return typeName == "exec" || typeName == "run" || typeName == "local"
}

func finalText(eventType string, event map[string]any) (string, bool) {
	isMessage := eventType == "agent_message" || eventType == "assistant_message" || eventType == "message" || eventType == "output_text" || eventType == "result" || strings.HasSuffix(eventType, ".output_text.done") || strings.HasSuffix(eventType, ".message.done")
	if !isMessage {
		return "", false
	}
	if text, ok := event["text"].(string); ok {
		return text, true
	}
	if text, ok := event["result"].(string); ok {
		return text, true
	}
	if text := contentText(event["content"]); text != "" {
		return text, true
	}
	return "", false
}

func contentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, child := range typed {
			if part := contentText(child); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "")
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		return contentText(typed["content"])
	default:
		return ""
	}
}
