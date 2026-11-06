//go:build !go1.27

package codec

import (
	"bytes"

	json "encoding/json"
)

type RawValue = json.RawMessage

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func unmarshalJSON(in []byte, out any) error {
	return json.Unmarshal(in, out)
}

func marshalParameters(v any) (*RawValue, error) {
	if v == nil {
		return nil, nil
	}
	switch raw := v.(type) {
	case *RawValue:
		if raw == nil {
			return nil, nil
		}
		copyRaw := append(RawValue(nil), (*raw)...)
		return &copyRaw, nil
	case RawValue:
		copyRaw := append(RawValue(nil), raw...)
		return &copyRaw, nil
	}
	b, err := marshalJSON(v)
	if err != nil {
		return nil, err
	}
	// Senders should omit the parameters member rather than transmitting
	// null or an empty object; all three encodings are equivalent.
	if string(bytes.TrimSpace(b)) == "{}" {
		return nil, nil
	}
	raw := RawValue(b)
	return &raw, nil
}

func decodeParameters(raw *RawValue, out any) error {
	if raw == nil || out == nil {
		return nil
	}
	return unmarshalJSON(*raw, out)
}
