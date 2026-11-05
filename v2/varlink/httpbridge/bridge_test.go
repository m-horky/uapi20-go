package httpbridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/varlink/go/v2/varlink"
)

const testInterface = `interface org.example.test

method Ping(message: string) -> (message: string)
method Fail() -> ()
method Denied() -> ()
method Watch() -> (index: int)
method NotImplemented() -> ()
method TakeOver() -> ()
`

// startTestService serves a test service on a real unix socket and returns
// its address.
func startTestService(t *testing.T) string {
	t.Helper()

	builder := varlink.NewServerBuilder(varlink.ServerConfig{})
	builder.SetInfo(varlink.ServiceInfo{
		Vendor:  "Test Vendor",
		Product: "Test Product",
		Version: "1.0",
		URL:     "https://example.test",
	})
	iface, err := builder.Interface(testInterface)
	if err != nil {
		t.Fatal(err)
	}
	if err := iface.Unary("Ping", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Message string `json:"message"`
		}
		if err := call.Decode(&in); err != nil {
			return call.ReplyError(ctx, varlink.ErrorInvalidParameter, map[string]string{"parameter": "message"})
		}
		return call.Reply(ctx, struct {
			Message string `json:"message"`
		}{Message: "pong:" + in.Message})
	}); err != nil {
		t.Fatal(err)
	}
	if err := iface.Unary("Fail", func(ctx context.Context, call varlink.UnaryCall) error {
		return call.ReplyError(ctx, "org.example.test.CustomError", map[string]string{"detail": "boom"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := iface.Unary("Denied", func(ctx context.Context, call varlink.UnaryCall) error {
		return call.ReplyError(ctx, varlink.ErrorPermissionDenied, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := iface.Stream("Watch", func(ctx context.Context, call varlink.StreamCall) error {
		for i := 0; i < 3; i++ {
			if err := call.Send(ctx, struct {
				Index int `json:"index"`
			}{Index: i}); err != nil {
				return err
			}
		}
		return call.Close(ctx, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := iface.Upgrade("TakeOver", func(ctx context.Context, call varlink.UpgradeCall) error {
		conn, err := call.Accept(ctx, nil)
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			buf := make([]byte, 512)
			for {
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				if _, err := conn.Write(buf[:n]); err != nil {
					return
				}
			}
		}()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// NotImplemented stays unregistered: the interface declares it, the
	// service does not implement it.

	server := builder.Build()
	path := filepath.Join(t.TempDir(), "service.sock")
	ln, err := server.Listen(context.Background(), "unix:"+path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = ln.Serve(context.Background())
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return "unix:" + path
}

func startTestBridge(t *testing.T, mutate func(*Config)) *httptest.Server {
	t.Helper()
	address := startTestService(t)
	cfg := Config{Services: map[string]string{
		"org.example.test": address,
	}}
	if mutate != nil {
		mutate(&cfg)
	}
	bridge, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(bridge.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postCall(t *testing.T, url string, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, payload
}

func TestHealth(t *testing.T) {
	ts := startTestBridge(t, nil)
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServicesIntrospection(t *testing.T) {
	ts := startTestBridge(t, nil)

	resp, err := http.Get(ts.URL + "/services")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var services struct {
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&services); err != nil {
		t.Fatal(err)
	}
	if len(services.Services) != 1 || services.Services[0] != "org.example.test" {
		t.Fatalf("services = %v", services.Services)
	}

	resp, err = http.Get(ts.URL + "/services/org.example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info struct {
		Vendor     string   `json:"vendor"`
		Interfaces []string `json:"interfaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Vendor != "Test Vendor" {
		t.Fatalf("vendor = %q", info.Vendor)
	}
	if len(info.Interfaces) != 2 {
		t.Fatalf("interfaces = %v", info.Interfaces)
	}

	resp, err = http.Get(ts.URL + "/services/org.example.test/org.example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var methods struct {
		Methods []string `json:"methods"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&methods); err != nil {
		t.Fatal(err)
	}
	want := []string{"Ping", "Fail", "Denied", "Watch", "NotImplemented", "TakeOver"}
	if strings.Join(methods.Methods, ",") != strings.Join(want, ",") {
		t.Fatalf("methods = %v, want %v", methods.Methods, want)
	}

	// The builtin service interface is introspectable, too.
	resp, err = http.Get(ts.URL + "/services/org.example.test/org.varlink.service")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCallSimpleMode(t *testing.T) {
	ts := startTestBridge(t, nil)

	status, body := postCall(t, ts.URL+"/call/org.example.test.Ping", `{"message":"hi"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	var out struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Message != "pong:hi" {
		t.Fatalf("message = %q", out.Message)
	}
	if ct := contentTypeOf(t, ts.URL+"/call/org.example.test.Ping", `{}`); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
}

func contentTypeOf(t *testing.T, url, body string) string {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("Content-Type")
}

func TestCallEmptyBody(t *testing.T) {
	ts := startTestBridge(t, nil)
	status, body := postCall(t, ts.URL+"/call/org.varlink.service.GetInfo?service=org.example.test", ``)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	if !strings.Contains(string(body), `"Test Vendor"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestCallExplicitService(t *testing.T) {
	ts := startTestBridge(t, nil)
	status, body := postCall(t, ts.URL+"/call/org.example.test/org.example.test.Ping", `{"message":"x"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	if !strings.Contains(string(body), "pong:x") {
		t.Fatalf("body = %s", body)
	}
}

func TestCallErrors(t *testing.T) {
	ts := startTestBridge(t, nil)
	for _, test := range []struct {
		name    string
		url     string
		body    string
		status  int
		wantIn  string
		message bool
	}{
		{
			name:    "malformed json",
			url:     "/call/org.example.test.Ping",
			body:    `{`,
			status:  http.StatusBadRequest,
			message: true,
		},
		{
			name:    "non-object body",
			url:     "/call/org.example.test.Ping",
			body:    `[1]`,
			status:  http.StatusBadRequest,
			message: true,
		},
		{
			name:    "unqualified method",
			url:     "/call/Ping",
			body:    `{}`,
			status:  http.StatusBadRequest,
			message: true,
		},
		{
			name:    "unknown service",
			url:     "/call/org.example.none.Ping",
			body:    `{}`,
			status:  http.StatusNotFound,
			message: true,
		},
		{
			name:   "method not found",
			url:    "/call/org.example.test.Nope",
			body:   `{}`,
			status: http.StatusNotFound,
			wantIn: "org.varlink.service.MethodNotFound",
		},
		{
			name:   "method not implemented",
			url:    "/call/org.example.test.NotImplemented",
			body:   `{}`,
			status: http.StatusNotImplemented,
			wantIn: "org.varlink.service.MethodNotImplemented",
		},
		{
			name:   "invalid parameter",
			url:    "/call/org.example.test.Ping",
			body:   `{"wrong":1}`,
			status: http.StatusBadRequest,
			wantIn: "org.varlink.service.InvalidParameter",
		},
		{
			name:   "permission denied",
			url:    "/call/org.example.test.Denied",
			body:   `{}`,
			status: http.StatusForbidden,
			wantIn: "org.varlink.service.PermissionDenied",
		},
		{
			name:   "custom error",
			url:    "/call/org.example.test.Fail",
			body:   `{}`,
			status: http.StatusInternalServerError,
			wantIn: "org.example.test.CustomError",
		},
		{
			name:   "expected more",
			url:    "/call/org.example.test.Watch",
			body:   `{}`,
			status: http.StatusBadRequest,
			wantIn: "org.varlink.service.ExpectedMore",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, body := postCall(t, ts.URL+test.url, test.body)
			if status != test.status {
				t.Fatalf("status = %d, want %d, body %s", status, test.status, body)
			}
			if test.wantIn != "" && !strings.Contains(string(body), test.wantIn) {
				t.Fatalf("body = %s, want it to contain %q", body, test.wantIn)
			}
			if test.message && !strings.Contains(string(body), `"message"`) {
				t.Fatalf("body = %s, want a message member", body)
			}
		})
	}
}

func TestCallTransportFailure(t *testing.T) {
	// A service address nothing listens on.
	bridge, err := New(Config{Services: map[string]string{
		"org.example.test": "unix:" + filepath.Join(t.TempDir(), "missing.sock"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(bridge.Handler())
	defer ts.Close()

	status, body := postCall(t, ts.URL+"/call/org.example.test.Ping", `{}`)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body %s", status, body)
	}
}

func TestAuthorize(t *testing.T) {
	ts := startTestBridge(t, func(cfg *Config) {
		cfg.Authorize = func(r *http.Request) error {
			if r.Header.Get("Authorization") == "" {
				return errUnauthorized
			}
			return nil
		}
	})
	status, _ := postCall(t, ts.URL+"/call/org.example.test.Ping", `{}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/call/org.example.test.Ping", strings.NewReader(`{"message":"x"}`))
	req.Header.Set("Authorization", "Bearer x")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %s", resp.StatusCode, body)
	}
}

var errUnauthorized = &httpError{401}

type httpError struct{ code int }

func (*httpError) Error() string { return "unauthorized" }

func TestStreamingReplies(t *testing.T) {
	ts := startTestBridge(t, nil)
	req, _ := http.NewRequest("POST", ts.URL+"/call/org.example.test.Watch?more=true", strings.NewReader(`{}`))
	req.Header.Set("Accept", "application/json-seq")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json-seq" {
		t.Fatalf("content type = %q", ct)
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	records := bytes.Split(bytes.TrimSuffix(payload, []byte{'\n'}), []byte{'\n'})
	// Three streaming replies plus the final reply of the call.
	if len(records) != 4 {
		t.Fatalf("records = %d, want 4 (%q)", len(records), payload)
	}
	if string(records[3]) != "\x1e{}" {
		t.Fatalf("final record = %q, want an empty object", records[3])
	}
	records = records[:3]
	for i, record := range records {
		if record[0] != 0x1e {
			t.Fatalf("record %d does not start with a record separator: %q", i, record)
		}
		var out struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal(record[1:], &out); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if out.Index != i {
			t.Fatalf("record %d index = %d", i, out.Index)
		}
	}
}

// wsClient is a minimal RFC 6455 client for exercising the WebSocket mode.
type wsClient struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialWebSocket(t *testing.T, url string) *wsClient {
	t.Helper()

	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	host, path, _ := strings.Cut(strings.TrimPrefix(url, "http://"), "/")
	path = "/" + path

	tcp, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := tcp.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(tcp)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("upgrade status = %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return &wsClient{conn: tcp, br: br}
}

// writeFrame sends one masked client frame (text or binary).
func (c *wsClient) writeFrame(t *testing.T, opcode byte, payload []byte) {
	t.Helper()
	frame := []byte{0x80 | opcode}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		t.Fatal(err)
	}
	length := len(payload)
	switch {
	case length < 126:
		frame = append(frame, 0x80|byte(length))
	case length < 65536:
		frame = append(frame, 0x80|126, byte(length>>8), byte(length))
	default:
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		frame = append(append(frame, 0x80|127), ext[:]...)
	}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := c.conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

// readFrame reads one server frame and returns its payload.
func (c *wsClient) readFrame(t *testing.T) (byte, []byte) {
	t.Helper()
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.br, header); err != nil {
		t.Fatal(err)
	}
	opcode := header[0] & 0x0f
	length := int(header[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(c.br, ext); err != nil {
			t.Fatal(err)
		}
		length = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(c.br, ext); err != nil {
			t.Fatal(err)
		}
		length = int(binary.BigEndian.Uint64(ext))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		t.Fatal(err)
	}
	return opcode, payload
}

func (c *wsClient) close() { _ = c.conn.Close() }

func TestWebSocketMode(t *testing.T) {
	ts := startTestBridge(t, nil)
	ws := dialWebSocket(t, ts.URL+"/ws/services/org.example.test")
	defer ws.close()

	// A call split across two text frames: frames are concatenated into
	// the byte stream, and one frame need not contain one message.
	call := `{"method":"org.example.test.Ping","parameters":{"message":"split"}}` + "\x00"
	ws.writeFrame(t, 0x1, []byte(call[:20]))
	ws.writeFrame(t, 0x1, []byte(call[20:]))

	opcode, payload := ws.readFrame(t)
	if opcode != 0x2 {
		t.Fatalf("opcode = %#x, want binary", opcode)
	}
	if payload[len(payload)-1] != 0 {
		t.Fatalf("frame does not include the NUL terminator: %q", payload)
	}
	var reply struct {
		Parameters struct {
			Message string `json:"message"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(bytes.TrimSuffix(payload, []byte{0}), &reply); err != nil {
		t.Fatalf("reply %q: %v", payload, err)
	}
	if reply.Parameters.Message != "pong:split" {
		t.Fatalf("message = %q", reply.Parameters.Message)
	}
}

func TestWebSocketErrorReply(t *testing.T) {
	ts := startTestBridge(t, nil)
	ws := dialWebSocket(t, ts.URL+"/ws/services/org.example.test")
	defer ws.close()

	ws.writeFrame(t, 0x2, []byte(`{"method":"org.example.test.Fail"}`+"\x00"))
	_, payload := ws.readFrame(t)
	var reply struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSuffix(payload, []byte{0}), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "org.example.test.CustomError" {
		t.Fatalf("error = %q", reply.Error)
	}

	// The connection stays usable after an error reply.
	ws.writeFrame(t, 0x2, []byte(`{"method":"org.varlink.service.GetInfo"}`+"\x00"))
	_, payload = ws.readFrame(t)
	if !bytes.Contains(payload, []byte("Test Vendor")) {
		t.Fatalf("reply = %q", payload)
	}
}

func TestWebSocketUnknownService(t *testing.T) {
	ts := startTestBridge(t, nil)
	resp, err := http.Get(ts.URL + "/ws/services/org.example.none")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWebSocketProtocolUpgrade(t *testing.T) {
	ts := startTestBridge(t, nil)
	ws := dialWebSocket(t, ts.URL+"/ws/services/org.example.test")
	defer ws.close()

	// Confirm the upgrade, then speak a private protocol over the
	// connection.
	ws.writeFrame(t, 0x2, []byte(`{"method":"org.example.test.TakeOver","upgrade":true}`+"\x00"))
	_, payload := ws.readFrame(t)
	if bytes.Contains(payload, []byte("error")) {
		t.Fatalf("upgrade failed: %q", payload)
	}

	// After the confirmed upgrade the bridge forwards verbatim, in
	// frames of arbitrary size; frames need not align with messages
	// anymore.
	ws.writeFrame(t, 0x2, []byte("raw-"))
	ws.writeFrame(t, 0x2, []byte("payload"))
	var echoed []byte
	for string(echoed) != "raw-payload" {
		_, frame := ws.readFrame(t)
		echoed = append(echoed, frame...)
		if len(echoed) > 1024 {
			t.Fatalf("echo = %q, want %q", echoed, "raw-payload")
		}
	}
}

// TestMutualTLS serves the bridge with a self-signed CA that requires
// client certificates.
func TestMutualTLS(t *testing.T) {
	address := startTestService(t)

	caKey, caTemplate := makeCA(t)
	caPool := x509.NewCertPool()
	caPool.AddCert(caTemplate)

	serverCert := makeCert(t, caTemplate, caKey, "server", nil)
	clientCert := makeCert(t, caTemplate, caKey, "client", nil)

	bridge, err := New(Config{
		Services: map[string]string{"org.example.test": address},
		TLS: &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    caPool,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(bridge.Handler())
	ts.TLS = bridge.cfg.TLS
	ts.StartTLS()
	defer ts.Close()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:      caPool,
			Certificates: []tls.Certificate{clientCert},
		},
	}}
	resp, err := client.Post(ts.URL+"/call/org.example.test.Ping", "application/json",
		strings.NewReader(`{"message":"secure"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "pong:secure") {
		t.Fatalf("body = %s", body)
	}

	// Without a client certificate the TLS handshake fails.
	noCert := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: caPool},
	}}
	if _, err := noCert.Post(ts.URL+"/call/org.example.test.Ping", "application/json",
		strings.NewReader(`{}`)); err == nil {
		t.Fatal("request without client certificate succeeded")
	}
}

func makeCA(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

func makeCert(t *testing.T, ca *x509.Certificate, caKey *rsa.PrivateKey, cn string, dnsNames []string) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
}
