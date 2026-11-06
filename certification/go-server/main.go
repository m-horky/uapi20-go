// Command go-server is an example varlink service used for interop
// testing against the reference Python implementation. It implements the
// com.example.echo interface (see com.example.echo.varlink) and the
// org.varlink.certification interface.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	certdesc "github.com/varlink/go/certification"
	varlink "github.com/varlink/go/v2/varlink"
)

func main() {
	addr := flag.String("address", "unix:@go-interop-echo", "varlink service address")
	ifacePath := flag.String("interface", "", "path to the com.example.echo.varlink file")
	flag.Parse()

	echoDescription, err := loadDescription(*ifacePath, certdesc.EchoDescription)
	if err != nil {
		fatal(err)
	}

	builder := varlink.NewServerBuilder(varlink.ServerConfig{
		AcceptFileDescriptors: true,
		Logger:                logPrinter{},
	})
	builder.SetInfo(varlink.ServiceInfo{
		Vendor:  "Example",
		Product: "Go interop service",
		Version: "1",
		URL:     "https://example.com/go",
	})

	if err := registerEcho(builder, echoDescription); err != nil {
		fatal(err)
	}
	if err := registerCertification(builder, certdesc.CertificationDescription); err != nil {
		fatal(err)
	}

	server := builder.Build()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := server.Listen(ctx, *addr)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("go-server: listening on %s\n", listener.Addr())
	if err := listener.Serve(ctx); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func loadDescription(flagValue, embedded string) (string, error) {
	if flagValue == "" {
		return embedded, nil
	}
	b, err := os.ReadFile(flagValue)
	if err != nil {
		return "", fmt.Errorf("read interface description %s: %w", flagValue, err)
	}
	return string(b), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "go-server:", err)
	os.Exit(1)
}

type logPrinter struct{}

func (logPrinter) Printf(format string, v ...any) {
	fmt.Printf("go-server: "+format, v...)
}

// ---------------------------------------------------------------- echo ---

func registerEcho(b *varlink.ServerBuilder, description string) error {
	iface, err := b.Interface(description)
	if err != nil {
		return err
	}
	var regErrs []error
	reg := func(err error) {
		if err != nil {
			regErrs = append(regErrs, err)
		}
	}

	reg(iface.Unary("Ping", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Ping string `json:"ping"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Pong string `json:"pong"`
		}{Pong: in.Ping})
	}))

	reg(iface.Unary("Add", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			A int64 `json:"a"`
			B int64 `json:"b"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Sum int64 `json:"sum"`
		}{Sum: in.A + in.B})
	}))

	reg(iface.Unary("Divide", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			A float64 `json:"a"`
			B float64 `json:"b"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Quotient float64 `json:"quotient"`
		}{Quotient: in.A / in.B})
	}))

	reg(iface.Unary("Toggle", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Flag bool `json:"flag"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Flag bool `json:"flag"`
		}{Flag: !in.Flag})
	}))

	reg(iface.Unary("Join", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Items     []string `json:"items"`
			Separator *string  `json:"separator"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		sep := ""
		if in.Separator != nil {
			sep = *in.Separator
		}
		return call.Reply(ctx, struct {
			Joined string `json:"joined"`
		}{Joined: strings.Join(in.Items, sep)})
	}))

	reg(iface.Unary("Lookup", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ID int64 `json:"id"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ID != 7 {
			return call.ReplyError(ctx, "com.example.echo.NoSuchRecord", struct {
				ID int64 `json:"id"`
			}{ID: in.ID})
		}
		return call.Reply(ctx, struct {
			Record struct {
				ID     int64             `json:"id"`
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
				Tags   []string          `json:"tags"`
			} `json:"record"`
		}{Record: struct {
			ID     int64             `json:"id"`
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
			Tags   []string          `json:"tags"`
		}{
			ID:     7,
			Name:   "seven",
			Labels: map[string]string{"lang": "en", "odd": "yes"},
			Tags:   []string{"prime", "odd"},
		}})
	}))

	reg(iface.Unary("Words", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Text string `json:"text"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		seen := map[string]struct{}{}
		for _, w := range strings.Fields(in.Text) {
			seen[w] = struct{}{}
		}
		return call.Reply(ctx, struct {
			Words map[string]struct{} `json:"words"`
		}{Words: seen})
	}))

	reg(iface.Unary("Choose", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Mode string `json:"mode"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Mode string `json:"mode"`
		}{Mode: in.Mode})
	}))

	reg(iface.Unary("CountMembers", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Value map[string]any `json:"value"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Members int `json:"members"`
		}{Members: len(in.Value)})
	}))

	reg(iface.Unary("CheckFD", func(ctx context.Context, call varlink.UnaryCall) error {
		fds := call.FileDescriptors()
		for _, fd := range fds {
			_ = fd.Close()
		}
		return call.Reply(ctx, struct {
			Received bool `json:"received"`
		}{Received: len(fds) > 0})
	}))

	reg(iface.Unary("Fail", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Code int64 `json:"code"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.ReplyError(ctx, "com.example.echo.TooBusy", struct {
			RetryAfter int64 `json:"retry_after"`
		}{RetryAfter: in.Code})
	}))

	reg(iface.Unary("Notify", func(ctx context.Context, call varlink.UnaryCall) error {
		return nil
	}))

	reg(iface.Stream("Watch", func(ctx context.Context, call varlink.StreamCall) error {
		var in struct {
			Count int64 `json:"count"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		for i := int64(1); i <= in.Count; i++ {
			last := i == in.Count
			out := struct {
				Index int64 `json:"index"`
			}{Index: i}
			if last {
				return call.Close(ctx, out)
			}
			if err := call.Send(ctx, out); err != nil {
				return err
			}
		}
		return nil
	}))

	reg(iface.Upgrade("Bridge", func(ctx context.Context, call varlink.UpgradeCall) error {
		conn, err := call.Accept(ctx, nil)
		if err != nil {
			return err
		}
		defer conn.Close()
		// Line-echo protocol over the taken-over connection.
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				line := strings.TrimRight(string(buf[:n]), "\n")
				if line == "quit" {
					return nil
				}
				if _, err := conn.Write([]byte("go:" + line + "\n")); err != nil {
					return err
				}
			}
			if err != nil {
				return nil
			}
		}
	}))

	return errors.Join(regErrs...)
}

// -------------------------------------------------------- certification ---

type certState struct {
	clientID string
}

func registerCertification(b *varlink.ServerBuilder, description string) error {
	iface, err := b.Interface(description)
	if err != nil {
		return err
	}
	state := &certState{clientID: "client-1"}
	var regErrs []error
	reg := func(err error) {
		if err != nil {
			regErrs = append(regErrs, err)
		}
	}

	reg(iface.Unary("Start", func(ctx context.Context, call varlink.UnaryCall) error {
		return call.Reply(ctx, struct {
			ClientID string `json:"client_id"`
		}{ClientID: state.clientID})
	}))

	reg(iface.Unary("Test01", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string `json:"client_id"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		return call.Reply(ctx, struct {
			Bool bool `json:"bool"`
		}{Bool: true})
	}))

	reg(iface.Unary("Test02", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string `json:"client_id"`
			Bool     bool   `json:"bool"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if !in.Bool {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return call.Reply(ctx, struct {
			Int int64 `json:"int"`
		}{Int: 1})
	}))

	reg(iface.Unary("Test03", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string `json:"client_id"`
			Int      int64  `json:"int"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if in.Int != 1 {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return call.Reply(ctx, struct {
			Float float64 `json:"float"`
		}{Float: 1})
	}))

	reg(iface.Unary("Test04", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string  `json:"client_id"`
			Float    float64 `json:"float"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if in.Float != 1 {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return call.Reply(ctx, struct {
			String string `json:"string"`
		}{String: "ping"})
	}))

	reg(iface.Unary("Test05", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string `json:"client_id"`
			String   string `json:"string"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if in.String != "ping" {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return call.Reply(ctx, struct {
			Bool   bool    `json:"bool"`
			Int    int64   `json:"int"`
			Float  float64 `json:"float"`
			String string  `json:"string"`
		}{Bool: false, Int: 2, Float: 3.141592653589793, String: "a lot of string"})
	}))

	reg(iface.Unary("Test06", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string  `json:"client_id"`
			Bool     bool    `json:"bool"`
			Int      int64   `json:"int"`
			Float    float64 `json:"float"`
			String   string  `json:"string"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if in.Bool || in.Int != 2 || in.Float != 3.141592653589793 || in.String != "a lot of string" {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		type inner struct {
			Bool   bool    `json:"bool"`
			Int    int64   `json:"int"`
			Float  float64 `json:"float"`
			String string  `json:"string"`
		}
		return call.Reply(ctx, struct {
			Struct inner `json:"struct"`
		}{Struct: inner{Bool: false, Int: 2, Float: 3.141592653589793, String: "a lot of string"}})
	}))

	reg(iface.Unary("Test07", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string `json:"client_id"`
			Struct   struct {
				Bool   bool    `json:"bool"`
				Int    int64   `json:"int"`
				Float  float64 `json:"float"`
				String string  `json:"string"`
			} `json:"struct"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if in.Struct.Bool || in.Struct.Int != 2 || in.Struct.Float != 3.141592653589793 || in.Struct.String != "a lot of string" {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return call.Reply(ctx, struct {
			Map map[string]string `json:"map"`
		}{Map: map[string]string{"foo": "Foo", "bar": "Bar"}})
	}))

	reg(iface.Unary("Test08", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string            `json:"client_id"`
			Map      map[string]string `json:"map"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if len(in.Map) != 2 || in.Map["foo"] != "Foo" || in.Map["bar"] != "Bar" {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return call.Reply(ctx, struct {
			Set map[string]struct{} `json:"set"`
		}{Set: map[string]struct{}{"one": {}, "two": {}, "three": {}}})
	}))

	reg(iface.Unary("Test09", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string              `json:"client_id"`
			Set      map[string]struct{} `json:"set"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if len(in.Set) != 3 {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return call.Reply(ctx, struct {
			MyType certMyType `json:"mytype"`
		}{MyType: certMyType{
			Object:     json.RawMessage(`{"method":"org.varlink.certification.Test09","parameters":{"map":{"foo":"Foo","bar":"Bar"}}}`),
			Enum:       "two",
			Struct:     certPair{First: 1, Second: "2"},
			Array:      []string{"one", "two", "three"},
			Dictionary: map[string]string{"foo": "Foo", "bar": "Bar"},
			Stringset:  map[string]struct{}{"one": {}, "two": {}, "three": {}},
			Nullable:   nil,
			Interface: certInterface{
				Foo: &[]*map[string]string{nil, ptr(map[string]string{"foo": "foo", "bar": "bar"}), nil, ptr(map[string]string{"one": "foo", "two": "bar"})},
				Anon: struct {
					Foo bool `json:"foo"`
					Bar bool `json:"bar"`
				}{Foo: true, Bar: false},
			},
		}})
	}))

	reg(iface.Stream("Test10", func(ctx context.Context, call varlink.StreamCall) error {
		var in struct {
			ClientID string     `json:"client_id"`
			MyType   certMyType `json:"mytype"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		for i := 1; i < 10; i++ {
			if err := call.Send(ctx, struct {
				String string `json:"string"`
			}{String: fmt.Sprintf("Reply number %d", i)}); err != nil {
				return err
			}
		}
		return call.Close(ctx, struct {
			String string `json:"string"`
		}{String: "Reply number 10"})
	}))

	reg(iface.Unary("Test11", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID        string   `json:"client_id"`
			LastMoreReplies []string `json:"last_more_replies"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		if !call.IsOneway() || len(in.LastMoreReplies) != 10 || in.LastMoreReplies[0] != "Reply number 1" || in.LastMoreReplies[9] != "Reply number 10" {
			return call.ReplyError(ctx, "org.varlink.certification.CertificationError", nil)
		}
		return nil
	}))

	reg(iface.Unary("End", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			ClientID string `json:"client_id"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		if in.ClientID != state.clientID {
			return call.ReplyError(ctx, "org.varlink.certification.ClientIdError", nil)
		}
		return call.Reply(ctx, struct {
			AllOK bool `json:"all_ok"`
		}{AllOK: true})
	}))

	return errors.Join(regErrs...)
}

func ptr[T any](v T) *T { return &v }

type certInterface struct {
	Foo  *[]*map[string]string `json:"foo"`
	Anon struct {
		Foo bool `json:"foo"`
		Bar bool `json:"bar"`
	} `json:"anon"`
}

type certMyType struct {
	Object              json.RawMessage     `json:"object"`
	Enum                string              `json:"enum"`
	Struct              certPair            `json:"struct"`
	Array               []string            `json:"array"`
	Dictionary          map[string]string   `json:"dictionary"`
	Stringset           map[string]struct{} `json:"stringset"`
	Nullable            *string             `json:"nullable"`
	NullableArrayStruct *[]certPair         `json:"nullable_array_struct"`
	Interface           certInterface       `json:"interface"`
}

type certPair struct {
	First  int64  `json:"first"`
	Second string `json:"second"`
}
