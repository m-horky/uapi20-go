package codec

import (
	"bytes"
	"errors"
	"testing"
)

func TestWireWriteReadMessage(t *testing.T) {
	wireOut := NewWire(bytes.NewReader(nil), &bytes.Buffer{}, 64, 1024)
	buf := wireOut.writer.(*bytes.Buffer)

	params, err := MarshalParameters(struct {
		Value string `json:"value"`
	}{Value: "hello"})
	if err != nil {
		t.Fatalf("MarshalParameters() error = %v", err)
	}

	msg := Message{
		Method:     "org.example.test.Ping",
		Parameters: params,
		More:       true,
	}
	if err := wireOut.WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage() error = %v", err)
	}

	wireIn := NewWire(bytes.NewReader(buf.Bytes()), ioDiscard{}, 64, 1024)
	got, err := wireIn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if got.Method != msg.Method || !got.More {
		t.Fatalf("ReadMessage() = %+v, want %+v", got, msg)
	}
	var decoded struct {
		Value string `json:"value"`
	}
	if err := DecodeParameters(got.Parameters, &decoded); err != nil {
		t.Fatalf("DecodeParameters() error = %v", err)
	}
	if decoded.Value != "hello" {
		t.Fatalf("DecodeParameters() value = %q, want hello", decoded.Value)
	}
}

func TestMarshalParametersCopiesRawValue(t *testing.T) {
	raw := RawValue(`{"value":"original"}`)
	got, err := MarshalParameters(raw)
	if err != nil {
		t.Fatalf("MarshalParameters() value error = %v", err)
	}
	raw[10] = 'm'
	if string(*got) != `{"value":"original"}` {
		t.Fatalf("MarshalParameters() value aliased input: %s", *got)
	}

	raw = RawValue(`{"value":"pointer"}`)
	got, err = MarshalParameters(&raw)
	if err != nil {
		t.Fatalf("MarshalParameters() pointer error = %v", err)
	}
	raw[10] = 'm'
	if string(*got) != `{"value":"pointer"}` {
		t.Fatalf("MarshalParameters() pointer aliased input: %s", *got)
	}
}

func TestMarshalParametersMatchesV1WireEncoding(t *testing.T) {
	got, err := MarshalParameters(map[string]any{
		"nilMap":   map[string]string(nil),
		"nilSlice": []string(nil),
		"text":     "\u2028\u2029",
		"values": map[string]string{
			"b": "<tag>",
			"a": "&",
		},
	})
	if err != nil {
		t.Fatalf("MarshalParameters() error = %v", err)
	}
	want := `{"nilMap":null,"nilSlice":null,"text":"\u2028\u2029","values":{"a":"\u0026","b":"\u003ctag\u003e"}}`
	if string(*got) != want {
		t.Fatalf("MarshalParameters() = %s, want %s", *got, want)
	}
}

func TestMarshalParametersReplacesInvalidUTF8(t *testing.T) {
	got, err := MarshalParameters(map[string]string{
		"bad": string([]byte{0xff}),
	})
	if err != nil {
		t.Fatalf("MarshalParameters() error = %v", err)
	}
	var decoded map[string]string
	if err := DecodeParameters(got, &decoded); err != nil {
		t.Fatalf("DecodeParameters() error = %v", err)
	}
	if decoded["bad"] != "\ufffd" {
		t.Fatalf("decoded invalid UTF-8 = %q, want replacement rune", decoded["bad"])
	}
}

func TestWireResetReader(t *testing.T) {
	var out bytes.Buffer
	wire := NewWire(bytes.NewReader([]byte(`{"method":"one"}`+"\x00")), &out, 64, 1024)

	msg, err := wire.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() first error = %v", err)
	}
	if msg.Method != "one" {
		t.Fatalf("ReadMessage() first method = %q", msg.Method)
	}

	wire.ResetReader(bytes.NewReader([]byte(`{"method":"two"}` + "\x00")))
	msg, err = wire.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() second error = %v", err)
	}
	if msg.Method != "two" {
		t.Fatalf("ReadMessage() second method = %q", msg.Method)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func TestDecodeMessageAccepts(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		check func(Message) bool
	}{
		{"call", `{"method":"org.example.Ping","parameters":{"a":1}}`, func(m Message) bool {
			return m.Method == "org.example.Ping" && m.Parameters != nil && !m.Oneway && !m.More && !m.Upgrade
		}},
		{"reply", `{"parameters":{"a":1},"continues":true}`, func(m Message) bool {
			return m.Continues && m.Method == ""
		}},
		{"error reply", `{"error":"org.example.Failed","parameters":{"why":"x"}}`, func(m Message) bool {
			return m.Error == "org.example.Failed" && !m.Continues
		}},
		{"absent parameters", `{"method":"org.example.Ping"}`, func(m Message) bool {
			return m.Parameters == nil
		}},
		{"null parameters", `{"method":"org.example.Ping","parameters":null}`, func(m Message) bool {
			return m.Parameters == nil
		}},
		{"flags", `{"method":"org.example.Ping","oneway":true}`, func(m Message) bool {
			return m.Oneway
		}},
		{"whitespace", " { \"method\" : \"org.example.Ping\" } ", func(m Message) bool {
			return m.Method == "org.example.Ping"
		}},
		{"vendor extension", `{"method":"org.example.Ping","org.example.meta":{"trace":true}}`, func(m Message) bool {
			ext, ok := m.Extensions["org.example.meta"]
			return ok && string(ext) == `{"trace":true}`
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := DecodeMessage([]byte(tt.frame))
			if err != nil {
				t.Fatalf("DecodeMessage() error = %v", err)
			}
			if !tt.check(msg) {
				t.Fatalf("DecodeMessage() = %+v", msg)
			}
		})
	}
}

func TestDecodeMessageRejects(t *testing.T) {
	tests := []string{
		// Invalid JSON and non-object top-level values.
		``,
		`{`,
		`[1,2]`,
		`5`,
		`"string"`,
		`true`,
		// Unknown members without a reverse-domain name.
		`{"method":"m","unknown":1}`,
		`{"parameters":{},"x-flag":true}`,
		// Vendor extensions that are not objects.
		`{"method":"m","org.example":7}`,
		`{"method":"m","org.example":[1]}`,
		// Flags that are not booleans.
		`{"method":"m","more":"true"}`,
		`{"method":"m","more":1}`,
		`{"method":"m","oneway":null}`,
		// At most one of oneway, more and upgrade.
		`{"method":"m","oneway":true,"more":true}`,
		`{"method":"m","more":true,"upgrade":true}`,
		`{"method":"m","oneway":true,"upgrade":true}`,
		// Parameters must be an object.
		`{"method":"m","parameters":[1]}`,
		`{"method":"m","parameters":"x"}`,
		`{"method":"m","parameters":7}`,
		// Wrong member types.
		`{"method":7}`,
		`{"error":{"nested":true}}`,
		// An error reply must not continue a call.
		`{"error":"org.example.Failed","continues":true}`,
	}
	for _, frame := range tests {
		t.Run(frame, func(t *testing.T) {
			_, err := DecodeMessage([]byte(frame))
			if !errors.Is(err, ErrMalformedMessage) {
				t.Fatalf("DecodeMessage() error = %v, want ErrMalformedMessage", err)
			}
		})
	}
}

func TestMarshalParametersOmitsEmptyObject(t *testing.T) {
	got, err := MarshalParameters(struct{}{})
	if err != nil {
		t.Fatalf("MarshalParameters() error = %v", err)
	}
	if got != nil {
		t.Fatalf("MarshalParameters() = %s, want omitted", *got)
	}
	got, err = MarshalParameters(map[string]int{})
	if err != nil {
		t.Fatalf("MarshalParameters() error = %v", err)
	}
	if got != nil {
		t.Fatalf("MarshalParameters() = %s, want omitted", *got)
	}
}
