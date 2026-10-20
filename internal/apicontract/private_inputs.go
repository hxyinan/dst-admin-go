package apicontract

import "encoding/json"

const privateReferencePattern = "^@private:[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$"

// AIInputSchema permits private references for write-only string fields. The
// original OpenAPI validator still checks resolved values before execution.
func (op *Operation) AIInputSchema() (map[string]any, error) {
	op.aiOnce.Do(func() { op.aiSchema, op.aiErr = op.buildAIInputSchema() })
	return op.aiSchema, op.aiErr
}
func (op *Operation) buildAIInputSchema() (map[string]any, error) {
	input, err := op.ToolSchema()
	if err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			out := map[string]any{}
			for key, item := range v {
				out[key] = walk(item)
			}
			if v["writeOnly"] == true && v["type"] == "string" {
				return map[string]any{"anyOf": []any{out, map[string]any{"type": "string", "pattern": privateReferencePattern}}, "description": "Use a private input reference for sensitive values; the resolved value is checked against the original field schema."}
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, item := range v {
				out[i] = walk(item)
			}
			return out
		default:
			return value
		}
	}
	return walk(input).(map[string]any), nil
}

// RedactInput applies writeOnly semantics to operation records in addition to
// generic credential redaction (e.g. Steam codes are secrets, error codes aren't).
func (op *Operation) RedactInput(raw json.RawMessage) json.RawMessage {
	schema, err := op.ToolSchema()
	if err != nil {
		return json.RawMessage(`{}`)
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return json.RawMessage(`{}`)
	}
	defs, _ := schema["$defs"].(map[string]any)
	var walk func(any, map[string]any, int) any
	walk = func(value any, definition map[string]any, depth int) any {
		if depth > 32 {
			return "[REDACTED]"
		}
		if definition["writeOnly"] == true {
			return "[REDACTED]"
		}
		if ref, ok := definition["$ref"].(string); ok {
			const prefix = "#/$defs/"
			if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
				if target, ok := defs[ref[len(prefix):]].(map[string]any); ok {
					return walk(value, target, depth+1)
				}
			}
		}
		switch v := value.(type) {
		case map[string]any:
			props, _ := definition["properties"].(map[string]any)
			for key, item := range v {
				if field, ok := props[key].(map[string]any); ok {
					v[key] = walk(item, field, depth+1)
				}
			}
		case []any:
			if item, ok := definition["items"].(map[string]any); ok {
				for i, vv := range v {
					v[i] = walk(vv, item, depth+1)
				}
			}
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
			if alternatives, ok := definition[keyword].([]any); ok {
				for _, item := range alternatives {
					if candidate, ok := item.(map[string]any); ok {
						value = walk(value, candidate, depth+1)
					}
				}
			}
		}
		return value
	}
	result, _ := json.Marshal(walk(value, schema, 0))
	return result
}

func (op *Operation) SensitiveValues(raw json.RawMessage) []string {
	var original, redacted any
	if json.Unmarshal(raw, &original) != nil || json.Unmarshal(op.RedactInput(raw), &redacted) != nil {
		return nil
	}
	var values []string
	var walk func(any, any)
	walk = func(a, b any) {
		if text, ok := a.(string); ok && text != "" && b == "[REDACTED]" {
			values = append(values, text)
			return
		}
		switch v := a.(type) {
		case map[string]any:
			if other, ok := b.(map[string]any); ok {
				for key, item := range v {
					walk(item, other[key])
				}
			}
		case []any:
			if other, ok := b.([]any); ok {
				for i, item := range v {
					if i < len(other) {
						walk(item, other[i])
					}
				}
			}
		}
	}
	walk(original, redacted)
	return values
}
