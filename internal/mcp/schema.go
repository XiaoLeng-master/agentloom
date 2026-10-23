package mcp

import (
	"fmt"
	"reflect"
)

// validateArguments checks the supported JSON Schema subset before sending a tool call.
// Unsupported schema keywords fail closed so validation cannot silently weaken.
func validateArguments(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	return validateValue(schema, args, "arguments")
}

func validateValue(schema map[string]any, value any, path string) error {
	for key := range schema {
		switch key {
		case "type", "properties", "required", "additionalProperties", "enum", "description", "title", "default":
		default:
			return fmt.Errorf("%s: unsupported schema keyword %q", path, key)
		}
	}
	typ, ok := schema["type"].(string)
	if !ok {
		return fmt.Errorf("%s: missing or invalid schema type", path)
	}
	switch typ {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object", path)
		}
		properties := map[string]any{}
		if raw, exists := schema["properties"]; exists {
			var valid bool
			properties, valid = raw.(map[string]any)
			if !valid {
				return fmt.Errorf("%s: invalid properties schema", path)
			}
		}
		if raw, exists := schema["required"]; exists {
			required, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("%s: invalid required schema", path)
			}
			for _, item := range required {
				name, ok := item.(string)
				if !ok {
					return fmt.Errorf("%s: invalid required field", path)
				}
				if _, exists := obj[name]; !exists {
					return fmt.Errorf("%s: missing required field %q", path, name)
				}
			}
		}
		allowExtra := true
		if raw, exists := schema["additionalProperties"]; exists {
			var valid bool
			allowExtra, valid = raw.(bool)
			if !valid {
				return fmt.Errorf("%s: unsupported additionalProperties schema", path)
			}
		}
		for key, item := range obj {
			child, exists := properties[key]
			if !exists {
				if !allowExtra {
					return fmt.Errorf("%s: unknown field %q", path, key)
				}
				continue
			}
			childSchema, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s: invalid schema", path, key)
			}
			if err := validateValue(childSchema, item, path+"."+key); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected string", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected boolean", path)
		}
	case "number", "integer":
		n, ok := value.(float64)
		if !ok {
			switch v := value.(type) {
			case int:
				n = float64(v)
			case int64:
				n = float64(v)
			case float32:
				n = float64(v)
			default:
				return fmt.Errorf("%s: expected %s", path, typ)
			}
		}
		if typ == "integer" && (n < -9223372036854775808 || n >= 9223372036854775808 || n != float64(int64(n))) {
			return fmt.Errorf("%s: expected integer", path)
		}
	default:
		return fmt.Errorf("%s: unsupported type %q", path, typ)
	}
	if raw, exists := schema["enum"]; exists {
		choices, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s: invalid enum schema", path)
		}
		matched := false
		for _, choice := range choices {
			if reflect.DeepEqual(choice, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value outside enum", path)
		}
	}
	return nil
}
