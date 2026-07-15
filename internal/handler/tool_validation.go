package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// toolHistoryValidationError describes a client-payload problem without
// retaining message text. Tool name/call ID are safe correlation metadata and
// make future incidents diagnosable through the shared X-Request-ID.
type toolHistoryValidationError struct {
	Code       string
	Reason     string
	ToolName   string
	ToolCallID string
}

type declaredTool struct {
	name   string
	schema map[string]any
}

type observedToolCall struct {
	name      string
	messageNo int
	result    bool
}

// validateToolHistory applies provider-neutral invariants shared by OpenAI
// Chat Completions and Anthropic Messages:
//   - declarations and call IDs are unique;
//   - every call names a currently declared tool;
//   - arguments are JSON objects compatible with the declared input schema;
//   - every result refers to one earlier call and every historical call has a
//     single result.
//
// It deliberately does not impose Kiro-specific limits such as tool count,
// name pattern, or request byte size; those belong in CLIProxy's candidate
// preflight so another combo provider can still serve a valid public payload.
func validateToolHistory(payload map[string]any) *toolHistoryValidationError {
	declared, issue := collectDeclaredTools(payload["tools"])
	if issue != nil {
		return issue
	}

	messages, _ := payload["messages"].([]any)
	calls := make(map[string]*observedToolCall)

	registerCall := func(id, name string, arguments any, messageNo int) *toolHistoryValidationError {
		id = strings.TrimSpace(id)
		name = strings.TrimSpace(name)
		if id == "" {
			return &toolHistoryValidationError{Code: "invalid_tool_call_id", Reason: "tool call ID is required", ToolName: name}
		}
		if _, exists := calls[id]; exists {
			return &toolHistoryValidationError{Code: "duplicate_tool_call_id", Reason: "tool call ID must be unique", ToolName: name, ToolCallID: id}
		}
		tool, ok := declared[name]
		if !ok || name == "" {
			return &toolHistoryValidationError{Code: "undeclared_tool", Reason: "tool call name is not present in tools", ToolName: name, ToolCallID: id}
		}
		args, err := decodeToolArguments(arguments)
		if err != nil {
			return &toolHistoryValidationError{Code: "invalid_tool_arguments", Reason: err.Error(), ToolName: name, ToolCallID: id}
		}
		if err := validateJSONSchemaValue(args, tool.schema, "arguments"); err != nil {
			return &toolHistoryValidationError{Code: "invalid_tool_arguments", Reason: err.Error(), ToolName: name, ToolCallID: id}
		}
		calls[id] = &observedToolCall{name: name, messageNo: messageNo}
		return nil
	}

	registerResult := func(id string, messageNo int) *toolHistoryValidationError {
		id = strings.TrimSpace(id)
		call, ok := calls[id]
		if !ok || id == "" {
			return &toolHistoryValidationError{Code: "unmatched_tool_result", Reason: "tool result has no preceding tool call", ToolCallID: id}
		}
		if call.messageNo >= messageNo {
			return &toolHistoryValidationError{Code: "unmatched_tool_result", Reason: "tool result must follow its tool call", ToolName: call.name, ToolCallID: id}
		}
		if call.result {
			return &toolHistoryValidationError{Code: "duplicate_tool_result", Reason: "tool call has more than one result", ToolName: call.name, ToolCallID: id}
		}
		call.result = true
		return nil
	}

	for messageNo, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role == "assistant" {
			if toolCalls, ok := message["tool_calls"].([]any); ok {
				for _, rawCall := range toolCalls {
					call, _ := rawCall.(map[string]any)
					fn, _ := call["function"].(map[string]any)
					if issue := registerCall(stringValue(call["id"]), stringValue(fn["name"]), fn["arguments"], messageNo); issue != nil {
						return issue
					}
				}
			}
		}
		if role == "tool" {
			if issue := registerResult(stringValue(message["tool_call_id"]), messageNo); issue != nil {
				return issue
			}
		}

		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(block["type"]) {
			case "tool_use":
				if issue := registerCall(stringValue(block["id"]), stringValue(block["name"]), block["input"], messageNo); issue != nil {
					return issue
				}
			case "tool_result":
				if issue := registerResult(stringValue(block["tool_use_id"]), messageNo); issue != nil {
					return issue
				}
			}
		}
	}

	for id, call := range calls {
		if !call.result {
			return &toolHistoryValidationError{Code: "missing_tool_result", Reason: "historical tool call has no result", ToolName: call.name, ToolCallID: id}
		}
	}
	return nil
}

func collectDeclaredTools(raw any) (map[string]declaredTool, *toolHistoryValidationError) {
	declared := make(map[string]declaredTool)
	items, _ := raw.([]any)
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, &toolHistoryValidationError{Code: "invalid_tool_schema", Reason: "tool declaration must be an object"}
		}
		definition := item
		if fn, ok := item["function"].(map[string]any); ok {
			definition = fn
		}
		name := strings.TrimSpace(stringValue(definition["name"]))
		if name == "" {
			// Hosted tools such as web_search do not participate in function
			// call history and therefore need no declaration entry here.
			if toolType := stringValue(item["type"]); toolType != "" && toolType != "function" {
				continue
			}
			return nil, &toolHistoryValidationError{Code: "invalid_tool_schema", Reason: "function tool name is required"}
		}
		if _, duplicate := declared[name]; duplicate {
			return nil, &toolHistoryValidationError{Code: "duplicate_tool_name", Reason: "tool declaration name must be unique", ToolName: name}
		}
		rawSchema := definition["parameters"]
		if rawSchema == nil {
			rawSchema = definition["input_schema"]
		}
		schema := map[string]any{}
		if rawSchema != nil {
			var schemaOK bool
			schema, schemaOK = rawSchema.(map[string]any)
			if !schemaOK {
				return nil, &toolHistoryValidationError{Code: "invalid_tool_schema", Reason: "tool input schema must be an object", ToolName: name}
			}
		}
		declared[name] = declaredTool{name: name, schema: schema}
	}
	return declared, nil
}

func decodeToolArguments(raw any) (map[string]any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	if args, ok := raw.(map[string]any); ok {
		return args, nil
	}
	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.UseNumber()
	var args map[string]any
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("tool arguments are not valid JSON: %w", err)
	}
	if args == nil {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}
	return args, nil
}

func validateJSONSchemaValue(value any, schema map[string]any, path string) error {
	if len(schema) == 0 {
		return nil
	}
	if enumValues, ok := schema["enum"].([]any); ok && len(enumValues) > 0 {
		matched := false
		for _, candidate := range enumValues {
			if fmt.Sprint(candidate) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed enum value", path)
		}
	}

	types := schemaTypes(schema["type"])
	if len(types) > 0 {
		matched := false
		for _, expected := range types {
			if valueMatchesJSONType(value, expected) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s has the wrong JSON type", path)
		}
	}

	object, isObject := value.(map[string]any)
	if isObject {
		if required, ok := schema["required"].([]any); ok {
			for _, rawName := range required {
				name := stringValue(rawName)
				if name != "" {
					if _, exists := object[name]; !exists {
						return fmt.Errorf("%s.%s is required", path, name)
					}
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, childValue := range object {
			rawChildSchema, declared := properties[name]
			if !declared {
				if allow, ok := schema["additionalProperties"].(bool); ok && !allow {
					return fmt.Errorf("%s.%s is not declared", path, name)
				}
				continue
			}
			childSchema, _ := rawChildSchema.(map[string]any)
			if err := validateJSONSchemaValue(childValue, childSchema, path+"."+name); err != nil {
				return err
			}
		}
	}
	if array, ok := value.([]any); ok {
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range array {
				if err := validateJSONSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func schemaTypes(raw any) []string {
	switch typed := raw.(type) {
	case string:
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := stringValue(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func valueMatchesJSONType(value any, expected string) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch value.(type) {
		case json.Number, float64, float32, int, int32, int64:
			return true
		}
	case "integer":
		switch number := value.(type) {
		case json.Number:
			_, err := number.Int64()
			return err == nil
		case float64:
			return math.Trunc(number) == number
		case float32:
			return float32(math.Trunc(float64(number))) == number
		case int, int32, int64:
			return true
		}
	case "null":
		return value == nil
	}
	return false
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
