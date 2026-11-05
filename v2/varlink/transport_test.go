package varlink

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		in      string
		network string
		path    string
		host    string
		port    string
	}{
		{"unix:/run/org.example", "unix", "/run/org.example", "", ""},
		{"unix:@org.example", "unix", "@org.example", "", ""},
		{"tcp:localhost:12345", "tcp", "", "localhost", "12345"},
		{"tcp:127.0.0.1:12345", "tcp", "", "127.0.0.1", "12345"},
		{"tcp:[::1]:12345", "tcp", "", "::1", "12345"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			addr, err := ParseAddress(tt.in)
			if err != nil {
				t.Fatalf("ParseAddress() error = %v", err)
			}
			if addr.Network != tt.network || addr.Path != tt.path || addr.Host != tt.host || addr.Port != tt.port {
				t.Fatalf("ParseAddress() = %+v", addr)
			}
			if len(addr.Props) != 0 {
				t.Fatalf("ParseAddress() props = %v", addr.Props)
			}
		})
	}
}

func TestParseAddressProperties(t *testing.T) {
	addr, err := ParseAddress("unix:/run/org.example;timeout=5;future=unknown")
	if err != nil {
		t.Fatalf("ParseAddress() error = %v", err)
	}
	if addr.Path != "/run/org.example" {
		t.Fatalf("Path = %q", addr.Path)
	}
	if got, want := len(addr.Props), 2; got != want {
		t.Fatalf("props len = %d, want %d", got, want)
	}
	if addr.Props["timeout"] != "5" || addr.Props["future"] != "unknown" {
		t.Fatalf("props = %v", addr.Props)
	}
	if _, err := ParseAddress("unix:/run/org.example;novalue"); err == nil {
		t.Fatal("ParseAddress() unexpectedly accepted malformed property")
	}
}

func TestParseAddressErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"/run/org.example",
		"unix:",
		"tcp:localhost",
		"tcp:localhost:",
		"vsock:1",
	} {
		if _, err := ParseAddress(in); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("ParseAddress(%q) error = %v, want ErrInvalidAddress", in, err)
		}
	}
}

func TestAddressString(t *testing.T) {
	for _, in := range []string{
		"unix:/run/org.example",
		"unix:@org.example",
		"tcp:localhost:12345",
		"tcp:127.0.0.1:12345",
		"tcp:[::1]:12345",
	} {
		addr, err := ParseAddress(in)
		if err != nil {
			t.Fatalf("ParseAddress(%q) error = %v", in, err)
		}
		if got := addr.String(); got != in {
			t.Errorf("Address.String() = %q, want %q", got, in)
		}
	}
}

func TestDialUnix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org.example.test")

	server, addr := newTestListenerServer(t, "unix:"+path)

	var out struct {
		Message string `json:"message"`
	}
	if err := server.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out.Message != "pong:hi" {
		t.Fatalf("Invoke() message = %q", out.Message)
	}
	if addr != path {
		t.Fatalf("listener address = %q, want %q", addr, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file missing while listening: %v", err)
	}
}

func TestDialAbstractUnix(t *testing.T) {
	const name = "@org.example.go.test.abstract"
	server, _ := newTestListenerServer(t, "unix:"+name)

	var out struct {
		Message string `json:"message"`
	}
	if err := server.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out.Message != "pong:hi" {
		t.Fatalf("Invoke() message = %q", out.Message)
	}
}

func TestDialTCP(t *testing.T) {
	server, _ := newTestListenerServer(t, "tcp:127.0.0.1:0")

	var out struct {
		Message string `json:"message"`
	}
	if err := server.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out.Message != "pong:hi" {
		t.Fatalf("Invoke() message = %q", out.Message)
	}
}

func TestDialErrors(t *testing.T) {
	if _, err := Dial(context.Background(), "bogus"); !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("Dial() error = %v, want ErrInvalidAddress", err)
	}
	if _, err := Dial(context.Background(), "unix:/nonexistent/org.example"); err == nil {
		t.Fatal("Dial() to missing socket unexpectedly succeeded")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := Dial(ctx, "tcp:127.0.0.1:1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dial() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestDialWithConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org.example.test")
	newTestListenerServer(t, "unix:"+path)

	client, err := DialWithConfig(context.Background(), "unix:"+path, ClientConfig{})
	if err != nil {
		t.Fatalf("DialWithConfig() error = %v", err)
	}
	defer client.Close()

	var out struct {
		Message string `json:"message"`
	}
	if err := client.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out.Message != "pong:hi" {
		t.Fatalf("Invoke() message = %q", out.Message)
	}
}

func TestListenerCloseRemovesUnixSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org.example.test")

	server := NewServerBuilder(ServerConfig{})
	mustInterface(t, server, pingDescription())
	listener, err := server.Build().Listen(context.Background(), "unix:"+path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file missing: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file still exists after Close(): %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestListenerRemovesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org.example.test")

	// A leftover socket file is removed before listening.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	stale.Close()

	server := NewServerBuilder(ServerConfig{})
	mustInterface(t, server, pingDescription())
	listener, err := server.Build().Listen(context.Background(), "unix:"+path)
	if err != nil {
		t.Fatalf("Listen() with stale socket error = %v", err)
	}
	defer listener.Close()
}

func TestListenerRejectsNonSocketPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(path, []byte("not a socket"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	server := NewServerBuilder(ServerConfig{})
	mustInterface(t, server, pingDescription())
	_, err := server.Build().Listen(context.Background(), "unix:"+path)
	if !errors.Is(err, ErrAddressInUse) {
		t.Fatalf("Listen() error = %v, want ErrAddressInUse", err)
	}
}

func TestListenerServeStopsOnContextCancel(t *testing.T) {
	server := NewServerBuilder(ServerConfig{})
	mustInterface(t, server, pingDescription())
	listener, err := server.Build().Listen(context.Background(), "tcp:127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- listener.Serve(ctx)
	}()

	cancel()
	select {
	case err := <-serveDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after context cancellation")
	}
}

func TestListenerServeStopsOnClose(t *testing.T) {
	server := NewServerBuilder(ServerConfig{})
	mustInterface(t, server, pingDescription())
	listener, err := server.Build().Listen(context.Background(), "tcp:127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- listener.Serve(context.Background())
	}()

	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after Close()")
	}
}

func TestListenerServesConcurrentClients(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org.example.test")
	newTestListenerServer(t, "unix:"+path)

	clients := make([]*ConnClient, 4)
	defer func() {
		for _, client := range clients {
			client.Close()
		}
	}()
	for i := range clients {
		client, err := Dial(context.Background(), "unix:"+path)
		if err != nil {
			t.Fatalf("Dial() error = %v", err)
		}
		clients[i] = client
	}
	for i, client := range clients {
		var out struct {
			Message string `json:"message"`
		}
		if err := client.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err != nil {
			t.Fatalf("client %d Invoke() error = %v", i, err)
		}
		if out.Message != "pong:hi" {
			t.Fatalf("client %d message = %q", i, out.Message)
		}
	}
}

func pingDescription() string {
	return "interface org.example.test\n\nmethod Ping(message: string) -> (message: string)\n"
}

// newTestListenerServer starts a server on address and returns a connected
// client plus the listener's resolved address.
func newTestListenerServer(t *testing.T, address string) (*ConnClient, string) {
	t.Helper()

	builder := NewServerBuilder(ServerConfig{})
	iface := mustInterface(t, builder, pingDescription())
	if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
		var in struct {
			Message string `json:"message"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Message string `json:"message"`
		}{Message: "pong:" + in.Message})
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	listener, err := builder.Build().Listen(context.Background(), address)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		if err := listener.Serve(context.Background()); err != nil {
			t.Errorf("Serve() error = %v", err)
		}
	}()

	// Resolve the bound address for dynamic ports.
	addr := listener.Addr().String()
	if listener.Addr().Network() == "tcp" {
		address = "tcp:" + addr
	}

	client, err := Dial(context.Background(), address)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, addr
}

func TestListenerRefusesLiveSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org.example.test")

	// A socket another process is still serving must not be removed.
	live, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer live.Close()

	server := NewServerBuilder(ServerConfig{})
	mustInterface(t, server, pingDescription())
	_, err = server.Build().Listen(context.Background(), "unix:"+path)
	if !errors.Is(err, ErrAddressInUse) {
		t.Fatalf("Listen() error = %v, want ErrAddressInUse", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live socket removed: %v", err)
	}
}

func TestListenerConnectionLimit(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{MaxConnections: 1})
	iface := mustInterface(t, builder, pingDescription())
	if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
		var in struct {
			Message string `json:"message"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Message string `json:"message"`
		}{Message: "pong:" + in.Message})
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}
	listener, err := builder.Build().Listen(context.Background(), "tcp:127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	go listener.Serve(context.Background())

	dial := func() *ConnClient {
		t.Helper()
		client, err := Dial(context.Background(), "tcp:"+listener.Addr().String())
		if err != nil {
			t.Fatalf("Dial() error = %v", err)
		}
		return client
	}

	first := dial()
	defer first.Close()
	var out struct {
		Message string `json:"message"`
	}
	if err := first.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err != nil {
		t.Fatalf("first Invoke() error = %v", err)
	}

	// The second connection is accepted by the kernel backlog but closed
	// by the server, failing its first call.
	second := dial()
	defer second.Close()
	if err := second.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err == nil {
		t.Fatal("Invoke() on a connection beyond the limit unexpectedly succeeded")
	}

	// Closing the first connection frees its slot.
	if err := first.Close(); err != nil {
		t.Fatalf("first.Close() error = %v", err)
	}
	third := dial()
	defer third.Close()
	if err := third.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "hi"}, &out); err != nil {
		t.Fatalf("Invoke() after freeing a slot error = %v", err)
	}
}
