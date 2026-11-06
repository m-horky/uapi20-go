// Command go-client is an example varlink client used for interop
// testing. It runs a battery of calls against a varlink service and prints
// one canonical result line per check for the interop harness to validate.
//
// Usage:
//
//	go-client -address unix:@go-interop-echo          # echo battery
//	go-client -address ... -certify                   # certification scenario
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/m-horky/uapi20-go/v2/varlink"
)

func main() {
	addr := flag.String("address", "unix:@go-interop-echo", "varlink service address")
	certify := flag.Bool("certify", false, "run the org.varlink.certification scenario")
	single := flag.Bool("single", false, "run a single Ping call only (for bad-server probing)")
	flag.Parse()

	conn, err := dial(*addr)
	if err != nil {
		fatal(err)
	}
	client := varlink.NewClient(conn, varlink.ClientConfig{
		AcceptFileDescriptors: true,
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if *single {
		var out struct {
			Pong string `json:"pong"`
		}
		err := client.Invoke(ctx, "com.example.echo.Ping", struct {
			Ping string `json:"ping"`
		}{Ping: "x"}, &out)
		fmt.Printf("[single Ping] %s\n", result(err, func() string { return out.Pong }))
		return
	}

	if *certify {
		runCertification(ctx, client)
		return
	}
	runEchoBattery(ctx, client)
}

func dial(address string) (net.Conn, error) {
	addr, err := varlink.ParseAddress(address)
	if err != nil {
		return nil, err
	}
	switch addr.Network {
	case "unix":
		path := addr.Path
		if strings.HasPrefix(path, "@") {
			path = "\x00" + path[1:]
		}
		return net.DialTimeout("unix", path, 5*time.Second)
	case "tcp":
		return net.DialTimeout("tcp", net.JoinHostPort(addr.Host, addr.Port), 5*time.Second)
	default:
		return nil, fmt.Errorf("unsupported network %q", addr.Network)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "go-client:", err)
	os.Exit(1)
}

// ---------------------------------------------------------------- echo ---

type check struct {
	name   string
	result string
}

func runEchoBattery(ctx context.Context, client *varlink.ConnClient) {
	var checks []check
	record := func(name, format string, args ...any) {
		checks = append(checks, check{name: name, result: fmt.Sprintf(format, args...)})
	}

	// 01: GetInfo
	info, ifaces, err := client.GetInfo(ctx)
	if err != nil {
		record("01 GetInfo", "ERROR %v", err)
	} else {
		sort.Strings(ifaces)
		record("01 GetInfo", "vendor=%q product=%q version=%q url=%q interfaces=%v",
			info.Vendor, info.Product, info.Version, info.URL, ifaces)
	}

	// 02: GetInterfaceDescription for the echo interface
	desc, err := client.GetInterfaceDescription(ctx, "com.example.echo")
	if err != nil {
		record("02 GetInterfaceDescription", "ERROR %v", err)
	} else {
		record("02 GetInterfaceDescription", "description starts %q", firstLine(desc))
	}

	// 03: Ping with unicode and HTML-sensitive characters
	var ping struct {
		Pong string `json:"pong"`
	}
	err = client.Invoke(ctx, "com.example.echo.Ping", struct {
		Ping string `json:"ping"`
	}{Ping: "pøing <&> ✓"}, &ping)
	record("03 Ping", "%s", result(err, func() string { return ping.Pong }))

	// 04: Add
	var add struct {
		Sum int64 `json:"sum"`
	}
	err = client.Invoke(ctx, "com.example.echo.Add", struct {
		A int64 `json:"a"`
		B int64 `json:"b"`
	}{A: 2, B: 40}, &add)
	record("04 Add", "%s", result(err, func() string { return fmt.Sprint(add.Sum) }))

	// 05: Divide
	var div struct {
		Quotient float64 `json:"quotient"`
	}
	err = client.Invoke(ctx, "com.example.echo.Divide", struct {
		A float64 `json:"a"`
		B float64 `json:"b"`
	}{A: 1, B: 8}, &div)
	record("05 Divide", "%s", result(err, func() string { return fmt.Sprintf("%.3f", div.Quotient) }))

	// 06: Toggle
	var toggle struct {
		Flag bool `json:"flag"`
	}
	err = client.Invoke(ctx, "com.example.echo.Toggle", struct {
		Flag bool `json:"flag"`
	}{Flag: true}, &toggle)
	record("06 Toggle", "%s", result(err, func() string { return fmt.Sprint(toggle.Flag) }))

	// 07: Join without separator
	var join struct {
		Joined string `json:"joined"`
	}
	err = client.Invoke(ctx, "com.example.echo.Join", struct {
		Items     []string `json:"items"`
		Separator *string  `json:"separator"`
	}{Items: []string{"a", "b", "c"}}, &join)
	record("07 Join null-sep", "%s", result(err, func() string { return join.Joined }))

	// 07b: Join with separator
	sep := "-"
	err = client.Invoke(ctx, "com.example.echo.Join", struct {
		Items     []string `json:"items"`
		Separator *string  `json:"separator"`
	}{Items: []string{"a", "b", "c"}, Separator: &sep}, &join)
	record("07b Join sep", "%s", result(err, func() string { return join.Joined }))

	// 08: Lookup hit
	var lookup struct {
		Record struct {
			ID     int64             `json:"id"`
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
			Tags   []string          `json:"tags"`
		} `json:"record"`
	}
	err = client.Invoke(ctx, "com.example.echo.Lookup", struct {
		ID int64 `json:"id"`
	}{ID: 7}, &lookup)
	record("08 Lookup hit", "%s", result(err, func() string {
		tags := append([]string(nil), lookup.Record.Tags...)
		sort.Strings(tags)
		return fmt.Sprintf("{id:%d name:%q labels:%v tags:%v}",
			lookup.Record.ID, lookup.Record.Name, sortedMap(lookup.Record.Labels), tags)
	}))

	// 09: Lookup miss -> custom error
	err = client.Invoke(ctx, "com.example.echo.Lookup", struct {
		ID int64 `json:"id"`
	}{ID: 99}, nil)
	record("09 Lookup miss", "%s", errorResult(err))

	// 10: Words (string set)
	var words struct {
		Words map[string]struct{} `json:"words"`
	}
	err = client.Invoke(ctx, "com.example.echo.Words", struct {
		Text string `json:"text"`
	}{Text: "the quick brown fox the"}, &words)
	record("10 Words", "%s", result(err, func() string {
		out := make([]string, 0, len(words.Words))
		for w := range words.Words {
			out = append(out, w)
		}
		sort.Strings(out)
		return fmt.Sprint(out)
	}))

	// 11: Choose (enum)
	var choose struct {
		Mode string `json:"mode"`
	}
	err = client.Invoke(ctx, "com.example.echo.Choose", struct {
		Mode string `json:"mode"`
	}{Mode: "fast"}, &choose)
	record("11 Choose", "%s", result(err, func() string { return choose.Mode }))

	// 12: CountMembers (object parameter)
	var count struct {
		Members int `json:"members"`
	}
	err = client.Invoke(ctx, "com.example.echo.CountMembers", struct {
		Value map[string]any `json:"value"`
	}{Value: map[string]any{"a": 1, "b": 2, "c": 3}}, &count)
	record("12 CountMembers", "%s", result(err, func() string { return fmt.Sprint(count.Members) }))

	// 13: Fail -> custom error with parameters
	err = client.Invoke(ctx, "com.example.echo.Fail", struct {
		Code int64 `json:"code"`
	}{Code: 3}, nil)
	record("13 Fail", "%s", errorResult(err))

	// 14: Notify (oneway), then verify the connection is still usable
	err = client.Oneway(ctx, "com.example.echo.Notify", struct {
		Message string `json:"message"`
	}{Message: "hello"})
	followup := ""
	if err == nil {
		var p struct {
			Pong string `json:"pong"`
		}
		if err := client.Invoke(ctx, "com.example.echo.Ping", struct {
			Ping string `json:"ping"`
		}{Ping: "after-oneway"}, &p); err != nil {
			followup = "follow-up ERROR " + err.Error()
		} else {
			followup = "follow-up " + p.Pong
		}
	}
	record("14 Notify oneway", "%s (%s)", errorResult(err), followup)

	// 15: Watch (streaming)
	stream, err := client.Stream(ctx, "com.example.echo.Watch", struct {
		Count int64 `json:"count"`
	}{Count: 3})
	if err != nil {
		record("15 Watch", "ERROR %v", err)
	} else {
		indexes := []int64{}
		for {
			var out struct {
				Index int64 `json:"index"`
			}
			err := stream.Recv(ctx, &out)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				record("15 Watch", "ERROR %v", err)
				indexes = nil
				break
			}
			indexes = append(indexes, out.Index)
		}
		if indexes != nil {
			record("15 Watch", "OK %v", indexes)
		}
	}

	// 16: unknown method on known interface
	err = client.Invoke(ctx, "com.example.echo.Nosuch", nil, nil)
	record("16 MethodNotFound", "%s", errorResult(err))

	// 17: unknown interface
	err = client.Invoke(ctx, "com.example.nosuch.Ping", nil, nil)
	record("17 InterfaceNotFound", "%s", errorResult(err))

	// 18: unimplemented method (declared, no handler) -- none in echo;
	// use org.varlink.service on the certification interface instead when
	// present. Here: unknown parameter.
	err = client.Invoke(ctx, "com.example.echo.Ping", json.RawMessage(`{"ping":"x","bogus":1}`), nil)
	record("18 unknown-parameter", "%s", errorResult(err))

	// 19: wrong parameter type
	err = client.Invoke(ctx, "com.example.echo.Ping", json.RawMessage(`{"ping":123}`), nil)
	record("19 wrong-type", "%s", errorResult(err))

	// 20: missing required parameter
	err = client.Invoke(ctx, "com.example.echo.Ping", nil, nil)
	record("20 missing-parameter", "%s", errorResult(err))

	// 21: unary method called with more flag
	var moreReply struct {
		Pong string `json:"pong"`
	}
	nMore := 0
	moreErr := error(nil)
	stream, err = client.Stream(ctx, "com.example.echo.Ping", struct {
		Ping string `json:"ping"`
	}{Ping: "streamed"})
	if err != nil {
		record("21 more-on-unary", "ERROR %v", err)
	} else {
		for {
			var out struct {
				Pong string `json:"pong"`
			}
			err := stream.Recv(ctx, &out)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				moreErr = err
				break
			}
			moreReply = out
			nMore++
		}
		if moreErr != nil {
			record("21 more-on-unary", "ERROR %v", moreErr)
		} else {
			record("21 more-on-unary", "OK replies=%d pong=%q", nMore, moreReply.Pong)
		}
	}

	// 22: GetInterfaceDescription for org.varlink.service
	desc, err = client.GetInterfaceDescription(ctx, "org.varlink.service")
	if err != nil {
		record("22 service-description", "ERROR %v", err)
	} else {
		record("22 service-description", "description starts %q", firstLine(desc))
	}

	// 23 (Go-only): batched pipelined calls, with a oneway call mixed in
	batch := client.Batch()
	var b1, b2, b3 struct {
		Pong string `json:"pong"`
	}
	batch.Invoke("com.example.echo.Ping", struct {
		Ping string `json:"ping"`
	}{Ping: "batch-1"}, &b1)
	batch.Oneway("com.example.echo.Notify", struct {
		Message string `json:"message"`
	}{Message: "batch-notify"})
	batch.Invoke("com.example.echo.Ping", struct {
		Ping string `json:"ping"`
	}{Ping: "batch-2"}, &b2)
	batch.Invoke("com.example.echo.Ping", struct {
		Ping string `json:"ping"`
	}{Ping: "batch-3"}, &b3)
	err = batch.Send(ctx)
	record("23 batch (Go-only)", "%s", result(err, func() string {
		return fmt.Sprintf("[%s %s %s]", b1.Pong, b2.Pong, b3.Pong)
	}))

	// 24 (Go-only): file descriptor passing
	if client.SupportsFileDescriptors() {
		var fdReply struct {
			Received bool `json:"received"`
		}
		payload := mustTempFile("fd-payload")
		fds, ferr := client.InvokeFDs(ctx, "com.example.echo.CheckFD", nil, &fdReply, []*os.File{
			payload,
		})
		_ = payload.Close()
		_ = os.Remove(payload.Name())
		closeFiles(fds)
		record("24 fd-passing (Go-only)", "%s", result(ferr, func() string {
			return fmt.Sprintf("server received fds: %v (fds returned by server: %d)", fdReply.Received, len(fds))
		}))
	} else {
		record("24 fd-passing (Go-only)", "SKIP transport does not support fds")
	}

	// 25: method name without a dot
	err = client.Invoke(ctx, "Nosuchmethod", nil, nil)
	record("25 no-dot-method", "%s", errorResult(err))

	// 26 (Go-only, must be last): connection upgrade. The connection is
	// consumed by the upgraded protocol, so nothing may follow.
	upgraded, err := client.Upgrade(ctx, "com.example.echo.Bridge", nil, nil)
	if err != nil {
		record("26 upgrade (Go-only)", "%s", errorResult(err))
	} else {
		if _, err := upgraded.Write([]byte("hello\n")); err != nil {
			record("26 upgrade (Go-only)", "LOCAL write error %v", err)
		} else {
			buf := make([]byte, 128)
			n, rerr := upgraded.Read(buf)
			if rerr != nil {
				record("26 upgrade (Go-only)", "LOCAL read error %v", rerr)
			} else {
				record("26 upgrade (Go-only)", "OK raw-echo %q", string(buf[:n]))
			}
		}
		upgraded.Close()
	}

	for _, c := range checks {
		fmt.Printf("[%s] %s\n", c.name, c.result)
	}
}

func mustTempFile(content string) *os.File {
	f, err := os.CreateTemp("", "varlink-interop-fd")
	if err != nil {
		fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		fatal(err)
	}
	return f
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

func firstLine(s string) string {
	lines := strings.SplitN(strings.TrimSpace(s), "\n", 2)
	return lines[0]
}

func sortedMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// result formats a check outcome: the error, or the value from fn.
func result(err error, fn func() string) string {
	if err != nil {
		return errorResult(err)
	}
	return "OK " + fn()
}

// errorResult renders an error canonically: remote errors as
// name(parameters), local errors as LOCAL <message>.
func errorResult(err error) string {
	if err == nil {
		return "OK"
	}
	var remote *varlink.RemoteError
	if errors.As(err, &remote) {
		params := "{}"
		if len(remote.Parameters) > 0 {
			var canonical any
			if json.Unmarshal(remote.Parameters, &canonical) == nil {
				if b, err := json.Marshal(canonical); err == nil {
					params = string(b)
				}
			}
		}
		return "REMOTE " + remote.Name + " " + params
	}
	return "LOCAL " + err.Error()
}

// -------------------------------------------------------- certification ---

func runCertification(ctx context.Context, client *varlink.ConnClient) {
	fail := func(step string, err error) {
		fmt.Printf("[cert %s] FAIL %s\n", step, errorResult(err))
		os.Exit(1)
	}
	ok := func(step string, format string, args ...any) {
		fmt.Printf("[cert %s] OK %s\n", step, fmt.Sprintf(format, args...))
	}

	var start struct {
		ClientID string `json:"client_id"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Start", nil, &start); err != nil {
		fail("Start", err)
	}
	ok("Start", "client_id=%q", start.ClientID)

	var test01 struct {
		Bool bool `json:"bool"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test01", struct {
		ClientID string `json:"client_id"`
	}{ClientID: start.ClientID}, &test01); err != nil {
		fail("Test01", err)
	}
	ok("Test01", "bool=%v", test01.Bool)

	var test02 struct {
		Int int64 `json:"int"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test02", struct {
		ClientID string `json:"client_id"`
		Bool     bool   `json:"bool"`
	}{ClientID: start.ClientID, Bool: test01.Bool}, &test02); err != nil {
		fail("Test02", err)
	}
	ok("Test02", "int=%d", test02.Int)

	var test03 struct {
		Float float64 `json:"float"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test03", struct {
		ClientID string `json:"client_id"`
		Int      int64  `json:"int"`
	}{ClientID: start.ClientID, Int: test02.Int}, &test03); err != nil {
		fail("Test03", err)
	}
	ok("Test03", "float=%v", test03.Float)

	var test04 struct {
		String string `json:"string"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test04", struct {
		ClientID string  `json:"client_id"`
		Float    float64 `json:"float"`
	}{ClientID: start.ClientID, Float: test03.Float}, &test04); err != nil {
		fail("Test04", err)
	}
	ok("Test04", "string=%q", test04.String)

	var test05 struct {
		Bool   bool    `json:"bool"`
		Int    int64   `json:"int"`
		Float  float64 `json:"float"`
		String string  `json:"string"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test05", struct {
		ClientID string `json:"client_id"`
		String   string `json:"string"`
	}{ClientID: start.ClientID, String: test04.String}, &test05); err != nil {
		fail("Test05", err)
	}
	ok("Test05", "bool=%v int=%d float=%v string=%q", test05.Bool, test05.Int, test05.Float, test05.String)

	var test06 struct {
		Struct struct {
			Bool   bool    `json:"bool"`
			Int    int64   `json:"int"`
			Float  float64 `json:"float"`
			String string  `json:"string"`
		} `json:"struct"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test06", struct {
		ClientID string  `json:"client_id"`
		Bool     bool    `json:"bool"`
		Int      int64   `json:"int"`
		Float    float64 `json:"float"`
		String   string  `json:"string"`
	}{
		ClientID: start.ClientID,
		Bool:     test05.Bool,
		Int:      test05.Int,
		Float:    test05.Float,
		String:   test05.String,
	}, &test06); err != nil {
		fail("Test06", err)
	}
	ok("Test06", "struct=%v", test06.Struct)

	var test07 struct {
		Map map[string]string `json:"map"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test07", struct {
		ClientID string `json:"client_id"`
		Struct   struct {
			Bool   bool    `json:"bool"`
			Int    int64   `json:"int"`
			Float  float64 `json:"float"`
			String string  `json:"string"`
		} `json:"struct"`
	}{ClientID: start.ClientID, Struct: test06.Struct}, &test07); err != nil {
		fail("Test07", err)
	}
	ok("Test07", "map=%v", sortedMap(test07.Map))

	var test08 struct {
		Set map[string]struct{} `json:"set"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test08", struct {
		ClientID string            `json:"client_id"`
		Map      map[string]string `json:"map"`
	}{ClientID: start.ClientID, Map: test07.Map}, &test08); err != nil {
		fail("Test08", err)
	}
	words := make([]string, 0, len(test08.Set))
	for w := range test08.Set {
		words = append(words, w)
	}
	sort.Strings(words)
	ok("Test08", "set=%v", words)

	var test09 struct {
		MyType certMyType `json:"mytype"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.Test09", struct {
		ClientID string              `json:"client_id"`
		Set      map[string]struct{} `json:"set"`
	}{ClientID: start.ClientID, Set: test08.Set}, &test09); err != nil {
		fail("Test09", err)
	}
	ok("Test09", "mytype verified=%v", verifyCertMyType(test09.MyType) == nil)

	stream, err := client.Stream(ctx, "org.varlink.certification.Test10", struct {
		ClientID string     `json:"client_id"`
		MyType   certMyType `json:"mytype"`
	}{ClientID: start.ClientID, MyType: test09.MyType})
	if err != nil {
		fail("Test10", err)
	}
	var replies []string
	for {
		var out struct {
			String string `json:"string"`
		}
		err := stream.Recv(ctx, &out)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail("Test10", err)
		}
		replies = append(replies, out.String)
	}
	ok("Test10", "replies=%d last=%q", len(replies), last(replies))

	if err := client.Oneway(ctx, "org.varlink.certification.Test11", struct {
		ClientID        string   `json:"client_id"`
		LastMoreReplies []string `json:"last_more_replies"`
	}{ClientID: start.ClientID, LastMoreReplies: replies}); err != nil {
		fail("Test11", err)
	}
	ok("Test11", "oneway sent")

	var end struct {
		AllOK bool `json:"all_ok"`
	}
	if err := client.Invoke(ctx, "org.varlink.certification.End", struct {
		ClientID string `json:"client_id"`
	}{ClientID: start.ClientID}, &end); err != nil {
		fail("End", err)
	}
	ok("End", "all_ok=%v", end.AllOK)
	fmt.Println("CERTIFICATION PASSED")
}

func last(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[len(ss)-1]
}

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

// verifyCertMyType checks the payload the way the official certification
// servers do; the values match the reference Python service.
func verifyCertMyType(m certMyType) error {
	var object struct {
		Method     string `json:"method"`
		Parameters struct {
			Map map[string]string `json:"map"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(m.Object, &object); err != nil {
		return err
	}
	if object.Method != "org.varlink.certification.Test09" {
		return errors.New("wrong object method")
	}
	if !reflect.DeepEqual(object.Parameters.Map, map[string]string{"foo": "Foo", "bar": "Bar"}) {
		return errors.New("wrong object parameters")
	}
	if m.Enum != "two" {
		return errors.New("wrong enum")
	}
	if m.Struct != (certPair{First: 1, Second: "2"}) {
		return errors.New("wrong struct")
	}
	if !reflect.DeepEqual(m.Array, []string{"one", "two", "three"}) {
		return errors.New("wrong array")
	}
	if !reflect.DeepEqual(m.Dictionary, map[string]string{"foo": "Foo", "bar": "Bar"}) {
		return errors.New("wrong dictionary")
	}
	if len(m.Stringset) != 3 {
		return errors.New("wrong stringset length")
	}
	if m.Nullable != nil {
		return errors.New("nullable must be nil")
	}
	if m.NullableArrayStruct != nil {
		return errors.New("nullable_array_struct must be nil")
	}
	if m.Interface.Foo == nil || len(*m.Interface.Foo) != 4 {
		return errors.New("wrong nested alias foo")
	}
	foo := *m.Interface.Foo
	if foo[0] != nil || foo[2] != nil {
		return errors.New("wrong nullable map entries")
	}
	if foo[1] == nil || !reflect.DeepEqual(*foo[1], map[string]string{"foo": "foo", "bar": "bar"}) {
		return errors.New("wrong foo[1]")
	}
	if foo[3] == nil || !reflect.DeepEqual(*foo[3], map[string]string{"one": "foo", "two": "bar"}) {
		return errors.New("wrong foo[3]")
	}
	if !m.Interface.Anon.Foo || m.Interface.Anon.Bar {
		return errors.New("wrong anon struct")
	}
	return nil
}
