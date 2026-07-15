package handler

import (
	"encoding/json"
	"testing"
)

func decodeToolTestPayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestValidateToolHistoryOpenAIValid(t *testing.T) {
	payload := decodeToolTestPayload(t, `{
        "tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"],"additionalProperties":false}}}],
        "messages":[
          {"role":"user","content":"find it"},
          {"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"docs\"}"}}]},
          {"role":"tool","tool_call_id":"call_1","content":"ok"},
          {"role":"user","content":"continue"}
        ]}`)
	if issue := validateToolHistory(payload); issue != nil {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestValidateToolHistoryAnthropicValid(t *testing.T) {
	payload := decodeToolTestPayload(t, `{
        "tools":[{"name":"Read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],
        "messages":[
          {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a.txt"}}]},
          {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"},{"type":"text","text":"next"}]}
        ]}`)
	if issue := validateToolHistory(payload); issue != nil {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestValidateToolHistoryRejectsStructuralProblems(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code string
	}{
		{
			name: "duplicate call id",
			raw:  `{"tools":[{"type":"function","function":{"name":"a"}}],"messages":[{"role":"assistant","tool_calls":[{"id":"same","function":{"name":"a","arguments":"{}"}},{"id":"same","function":{"name":"a","arguments":"{}"}}]}]}`,
			code: "duplicate_tool_call_id",
		},
		{
			name: "undeclared name",
			raw:  `{"tools":[{"type":"function","function":{"name":"search"}}],"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"shell","arguments":"{}"}}]}]}`,
			code: "undeclared_tool",
		},
		{
			name: "malformed arguments",
			raw:  `{"tools":[{"type":"function","function":{"name":"search"}}],"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"search","arguments":"{bad"}}]}]}`,
			code: "invalid_tool_arguments",
		},
		{
			name: "orphan result",
			raw:  `{"tools":[],"messages":[{"role":"tool","tool_call_id":"missing","content":"x"}]}`,
			code: "unmatched_tool_result",
		},
		{
			name: "missing result",
			raw:  `{"tools":[{"type":"function","function":{"name":"search"}}],"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"search","arguments":"{}"}}]},{"role":"user","content":"next"}]}`,
			code: "missing_tool_result",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issue := validateToolHistory(decodeToolTestPayload(t, tc.raw))
			if issue == nil || issue.Code != tc.code {
				t.Fatalf("issue=%#v want code=%s", issue, tc.code)
			}
		})
	}
}

func TestValidateToolHistoryAllowsHistoricalSchemaMismatchForRecovery(t *testing.T) {
	payload := decodeToolTestPayload(t, `{
        "tools":[{"name":"Glob","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}}],
        "messages":[
          {"role":"assistant","content":[{"type":"tool_use","id":"toolu_bad","name":"Glob","input":{}}]},
          {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_bad","is_error":true,"content":"pattern is required"}]}
        ]}`)
	if issue := validateToolHistory(payload); issue != nil {
		t.Fatalf("schema-mismatched historical call with an error result must remain recoverable: %+v", issue)
	}
}
