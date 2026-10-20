package aicontrol

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type PrivateInput struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}
type privateInputValue struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

const privateInputPrefix = "@private:"

func (s *Store) SavePrivateInput(p Principal, name, value string) (PrivateInput, error) {
	if strings.TrimSpace(name) == "" || len(name) > 100 || value == "" || len(value) > 8192 {
		return PrivateInput{}, ErrInvalid
	}
	raw, _ := json.Marshal(privateInputValue{Kind: "input", Name: strings.TrimSpace(name), Value: value})
	id, err := s.SavePrivateResult(p, raw)
	return PrivateInput{ID: id, Name: strings.TrimSpace(name), CreatedAt: time.Now().UTC()}, err
}
func (s *Store) privateInput(p Principal, id string) (PrivateInput, string, error) {
	raw, err := s.PrivateResult(p, id)
	if err != nil {
		return PrivateInput{}, "", err
	}
	var v privateInputValue
	if json.Unmarshal(raw, &v) != nil || v.Kind != "input" {
		return PrivateInput{}, "", ErrInvalid
	}
	return PrivateInput{ID: id, Name: v.Name}, v.Value, nil
}

// References are resolved only inside business request bodies immediately
// before schema validation and dispatch. Original arguments retain references.
func (s *Store) resolvePrivateInputs(p Principal, raw json.RawMessage) (json.RawMessage, []string, error) {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return nil, nil, ErrInvalid
	}
	var values []string
	bytes := 0
	var walk func(any) (any, error)
	walk = func(value any) (any, error) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				result, err := walk(item)
				if err != nil {
					return nil, err
				}
				v[key] = result
			}
		case []any:
			for i, item := range v {
				result, err := walk(item)
				if err != nil {
					return nil, err
				}
				v[i] = result
			}
		case string:
			if strings.HasPrefix(v, privateInputPrefix) {
				_, secret, err := s.privateInput(p, strings.TrimPrefix(v, privateInputPrefix))
				if err != nil {
					return nil, fmt.Errorf("%w: private input is expired or unavailable", ErrInvalid)
				}
				values = append(values, secret)
				value = secret
				v = secret
			}
			bytes += len(v)
			if bytes > 256<<10 {
				return nil, ErrInvalid
			}
		}
		return value, nil
	}
	value, err := walk(input["body"])
	if err != nil {
		return nil, nil, err
	}
	if _, ok := input["body"]; ok {
		input["body"] = value
	}
	result, err := json.Marshal(input)
	return result, values, err
}
func redactPrivateValues(raw json.RawMessage, values []string) json.RawMessage {
	if len(values) == 0 {
		return raw
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return json.RawMessage(`{"error":"AI_INVALID_RESULT"}`)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			for _, secret := range values {
				if secret != "" {
					x = strings.ReplaceAll(x, secret, "[REDACTED]")
				}
			}
			return x
		case map[string]any:
			for key, item := range x {
				x[key] = walk(item)
			}
		case []any:
			for i, item := range x {
				x[i] = walk(item)
			}
		}
		return v
	}
	result, _ := json.Marshal(walk(value))
	return result
}
