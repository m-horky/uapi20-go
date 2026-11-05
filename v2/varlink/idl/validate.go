package idl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// ValidationError describes call parameters that do not match an interface
// declaration.
type ValidationError struct {
	// Field is the dotted path to the offending field, or "" when the
	// parameters themselves are invalid.
	Field string
	// Reason describes the mismatch.
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return "idl: invalid parameters: " + e.Reason
	}
	return "idl: invalid parameter " + strconv.Quote(e.Field) + ": " + e.Reason
}

// ValidateCall checks that parameters match the declared input parameters of
// the named method. Absent, null and empty-object parameters are equivalent;
// a non-nullable field must be present and non-null, unknown fields are
// rejected, and values must match their declared types.
func (i *Interface) ValidateCall(method string, parameters []byte) error {
	m, ok := i.methods[method]
	if !ok {
		return fmt.Errorf("idl: method %q not declared", method)
	}
	return i.validate(m.In, parameters, "")
}

func (i *Interface) validate(t *Type, raw []byte, path string) error {
	raw = bytes.TrimSpace(raw)

	switch t.Kind {
	case Maybe:
		if len(raw) == 0 || isJSONNull(raw) {
			return nil // absent and null are equivalent for nullable types
		}
		return i.validate(t.Elem, raw, path)

	case Bool:
		if !isJSONBool(raw) {
			return typeMismatch(t, raw, path)
		}
		return nil

	case Int:
		if !isJSONInt(raw) {
			return typeMismatch(t, raw, path)
		}
		return nil

	case Float:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return typeMismatch(t, raw, path)
		}
		return nil

	case String:
		if !isJSONString(raw) {
			return typeMismatch(t, raw, path)
		}
		return nil

	case Object:
		if !isJSONObject(raw) {
			return typeMismatch(t, raw, path)
		}
		return nil

	case Any:
		// Any value except null.
		if len(raw) == 0 || isJSONNull(raw) {
			return &ValidationError{Field: path, Reason: "must not be null"}
		}
		return nil

	case Enum:
		var value string
		if !isJSONString(raw) || json.Unmarshal(raw, &value) != nil {
			return typeMismatch(t, raw, path)
		}
		for _, field := range t.Fields {
			if field.Name == value {
				return nil
			}
		}
		return &ValidationError{Field: path, Reason: "unknown enum value " + strconv.Quote(value)}

	case Array:
		if !isJSONArray(raw) {
			return typeMismatch(t, raw, path)
		}
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return &ValidationError{Field: path, Reason: "invalid array"}
		}
		for _, element := range elements {
			if err := i.validate(t.Elem, element, path); err != nil {
				return err
			}
		}
		return nil

	case Map:
		if !isJSONObject(raw) {
			return typeMismatch(t, raw, path)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return &ValidationError{Field: path, Reason: "invalid object"}
		}
		for key, value := range values {
			if err := i.validate(t.Elem, value, joinField(path, key)); err != nil {
				return err
			}
		}
		return nil

	case Struct:
		// Absent and null parameters are equivalent to an empty object.
		if len(raw) == 0 || isJSONNull(raw) {
			raw = []byte("{}")
		}
		if !isJSONObject(raw) {
			return typeMismatch(t, raw, path)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return &ValidationError{Field: path, Reason: "invalid object"}
		}
		for _, field := range t.Fields {
			value, present := values[field.Name]
			if !present || isJSONNull(bytes.TrimSpace(value)) {
				if field.Type.Kind != Maybe {
					return &ValidationError{Field: joinField(path, field.Name), Reason: "missing required field"}
				}
				continue
			}
			if err := i.validate(field.Type, value, joinField(path, field.Name)); err != nil {
				return err
			}
		}
		for key := range values {
			if !t.hasField(key) {
				return &ValidationError{Field: joinField(path, key), Reason: "unknown field"}
			}
		}
		return nil

	case Named:
		decl, ok := i.types[t.Name]
		if !ok {
			return &ValidationError{Field: path, Reason: "unknown type " + strconv.Quote(t.Name)}
		}
		return i.validate(decl.Type, raw, path)
	}

	return &ValidationError{Field: path, Reason: "unsupported type"}
}

func (t *Type) hasField(name string) bool {
	for _, field := range t.Fields {
		if field.Name == name {
			return true
		}
	}
	return false
}

func typeMismatch(t *Type, raw []byte, path string) error {
	return &ValidationError{Field: path, Reason: "value does not match type " + t.String()}
}

func joinField(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func isJSONNull(raw []byte) bool   { return string(raw) == "null" }
func isJSONBool(raw []byte) bool   { return string(raw) == "true" || string(raw) == "false" }
func isJSONString(raw []byte) bool { return len(raw) > 0 && raw[0] == '"' }
func isJSONObject(raw []byte) bool { return len(raw) > 0 && raw[0] == '{' }
func isJSONArray(raw []byte) bool  { return len(raw) > 0 && raw[0] == '[' }
func isJSONNumber(raw []byte) bool {
	return len(raw) > 0 && (raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9')
}

// isJSONInt reports whether raw is a JSON number without a fractional part
// or exponent.
func isJSONInt(raw []byte) bool {
	if !isJSONNumber(raw) {
		return false
	}
	for _, c := range raw {
		if c == '.' || c == 'e' || c == 'E' {
			return false
		}
	}
	return true
}
