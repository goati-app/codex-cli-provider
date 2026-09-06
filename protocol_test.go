package codexcli

import (
	"strings"
	"testing"
)

func TestParseCapturesUsageSearchAndIdentifiers(t *testing.T) {
	result, err := ParseJSONL([]byte("{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n"+
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"web_search\",\"action\":{\"type\":\"search\",\"queries\":[\"one\",\"two\"]}}}\n"+
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"ok\\\":true}\"}}\n"+
		"{\"type\":\"turn.completed\",\"model\":\"gpt-test\",\"usage\":{\"input_tokens\":12,\"cached_input_tokens\":7,\"output_tokens\":5,\"reasoning_output_tokens\":3}}\n"), DefaultJSONLMaxBytes, DefaultResultMaxBytes, DefaultMetadataMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TokensReported || result.InputTokens != 12 || result.OutputTokens != 5 || result.CachedInputTokens == nil || *result.CachedInputTokens != 7 || result.ReasoningOutputTokens == nil || *result.ReasoningOutputTokens != 3 || result.OfficialWebSearchCalls != 1 || result.InferredSearchQueries != 2 || result.ThreadID != "thread-1" || result.Model != "gpt-test" {
		t.Fatalf("result=%+v", result)
	}
}

func TestParseReturnsPartialDataOnMalformedTail(t *testing.T) {
	result, err := ParseJSONL([]byte("{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}\n{broken"), DefaultJSONLMaxBytes, DefaultResultMaxBytes, DefaultMetadataMaxBytes)
	if KindOf(err) != ProtocolInvalid || !result.TokensReported || result.InputTokens != 4 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestValidateWebSearch(t *testing.T) {
	tests := []struct {
		name   string
		result ProtocolResult
		max    int
		kind   ErrorKind
	}{
		{"missing", ProtocolResult{FinalMessageEvent: 1}, 3, ProtocolInvalid},
		{"too many", ProtocolResult{OfficialWebSearchCalls: 4, LastWebSearchEvent: 4, FinalMessageEvent: 5}, 3, ProtocolInvalid},
		{"late result required", ProtocolResult{OfficialWebSearchCalls: 1, LastWebSearchEvent: 3, FinalMessageEvent: 2}, 3, SchemaInvalid},
		{"valid", ProtocolResult{OfficialWebSearchCalls: 1, LastWebSearchEvent: 2, FinalMessageEvent: 3}, 3, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := KindOf(ValidateWebSearchResult(test.result, test.max)); got != test.kind {
				t.Fatalf("kind=%q want=%q", got, test.kind)
			}
		})
	}
}

func TestParseRejectsForbiddenEventsAndOversizedMetadata(t *testing.T) {
	_, err := ParseJSONL([]byte("{\"type\":\"command_execution\"}\n{\"type\":\"agent_message\",\"text\":\"ok\"}"), DefaultJSONLMaxBytes, DefaultResultMaxBytes, DefaultMetadataMaxBytes)
	if KindOf(err) != ProtocolInvalid {
		t.Fatalf("err=%v", err)
	}
	result, err := ParseJSONL([]byte("{\"type\":\"thread.started\",\"thread_id\":\""+strings.Repeat("x", DefaultMetadataMaxBytes+1)+"\"}\n{\"type\":\"agent_message\",\"text\":\"ok\"}"), DefaultJSONLMaxBytes, DefaultResultMaxBytes, DefaultMetadataMaxBytes)
	if KindOf(err) != ProtocolInvalid || result.ThreadID != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func FuzzParseNeverPanics(f *testing.F) {
	f.Add([]byte(`{"type":"agent_message","text":"ok"}`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseJSONL(data, DefaultJSONLMaxBytes, DefaultResultMaxBytes, DefaultMetadataMaxBytes)
	})
}
