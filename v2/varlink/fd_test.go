//go:build unix

package varlink

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

const fdTestInterface = `interface org.example.fds

method Echo(data: string) -> (length: int)
method Notify(data: string) -> ()
method Watch() -> (index: int)
method TakeOver() -> ()
`

// newUnixTestServer serves a fresh server over a real AF_UNIX socket and
// returns its address, so that socket features such as SCM_RIGHTS are
// exercised.
func newUnixTestServer(t *testing.T, cfg ServerConfig, register func(*ServerBuilder)) string {
	t.Helper()

	builder := NewServerBuilder(cfg)
	register(builder)
	server := builder.Build()

	path := newUnixTestSocketPath(t)
	ln, err := server.Listen(context.Background(), "unix:"+path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	go func() {
		if err := ln.Serve(context.Background()); err != nil && err != context.Canceled {
			t.Errorf("Serve() error = %v", err)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return "unix:" + path
}

func newFDTestClient(t *testing.T, addr string, accept bool) *ConnClient {
	t.Helper()
	client, err := DialWithConfig(context.Background(), addr, ClientConfig{AcceptFileDescriptors: accept})
	if err != nil {
		t.Fatalf("DialWithConfig() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestFileDescriptorRoundTrip passes a pipe write end to the service, which
// writes to it and replies with the read end of another pipe.
func TestFileDescriptorRoundTrip(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			fds := call.Files()
			if len(fds) != 1 {
				return call.ReplyError(ctx, ErrorInvalidParameter, map[string]any{"parameter": "fds"})
			}
			// Write through the descriptor the client passed.
			if _, err := fds[0].Write([]byte("ping")); err != nil {
				return err
			}
			// Reply with a descriptor of our own.
			r, w, err := os.Pipe()
			if err != nil {
				return err
			}
			// Write before replying (and before the deferred Close), so
			// the client cannot race the close.
			if _, err := w.Write([]byte("pong")); err != nil {
				return err
			}
			defer w.Close()
			if _, err := call.PushFiles(Files{r}); err != nil {
				return err
			}
			return call.Reply(ctx, struct {
				Length int `json:"length"`
			}{Length: 4})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, true)
	if !client.SupportsFileDescriptors() {
		t.Fatal("SupportsFileDescriptors() = false for AF_UNIX connection")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Length int `json:"length"`
	}
	var files Files
	err = client.Invoke(context.Background(), "org.example.fds.Echo", struct {
		Data string `json:"data"`
	}{Data: "x"}, &out, SendFiles(Files{w}), ReceiveFiles(&files))
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	_ = w.Close()
	if out.Length != 4 {
		t.Fatalf("reply length = %d, want 4", out.Length)
	}

	// The client's pipe must carry the service's write...
	buf := make([]byte, 4)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("reading client pipe: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("client pipe = %q, want %q", buf, "ping")
	}
	_ = r.Close()

	// ...and the received descriptor the service's write.
	if len(files) != 1 {
		t.Fatalf("received %d file descriptors, want 1", len(files))
	}
	if _, err := io.ReadFull(files[0], buf); err != nil {
		t.Fatalf("reading received descriptor: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("received descriptor = %q, want %q", buf, "pong")
	}
	_ = files[0].Close()
}

// TestFileDescriptorsNotAcceptedByDefault verifies that a service which did
// not opt into receiving file descriptors does not see them. On Linux 6.16
// and later the listener turns off SO_PASSRIGHTS, in which case the send
// itself is refused; on other kernels the descriptors are silently
// discarded when the connection is read.
func TestFileDescriptorsNotAcceptedByDefault(t *testing.T) {
	seen := make(chan int, 1)
	addr := newUnixTestServer(t, ServerConfig{}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			seen <- len(call.Files())
			return call.Reply(ctx, struct {
				Length int `json:"length"`
			}{Length: 0})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, false)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out struct {
		Length int `json:"length"`
	}
	err = client.Invoke(context.Background(), "org.example.fds.Echo", struct {
		Data string `json:"data"`
	}{Data: "x"}, &out, SendFiles(Files{w}))
	_ = w.Close()
	if err == nil {
		select {
		case n := <-seen:
			if n != 0 {
				t.Fatalf("handler saw %d file descriptors, want 0", n)
			}
		default:
			t.Fatal("handler did not run")
		}
	}
}

// TestFileDescriptorsUnsupportedTransport verifies graceful degradation on
// a transport that cannot carry file descriptors.
func TestFileDescriptorsUnsupportedTransport(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	builder := NewServerBuilder(ServerConfig{})
	server := builder.Build()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = server.Serve(context.Background(), conn) }()
		}
	}()

	client, err := DialWithConfig(context.Background(), "tcp:"+ln.Addr().String(), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if client.SupportsFileDescriptors() {
		t.Fatal("SupportsFileDescriptors() = true for TCP connection")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := client.Invoke(context.Background(), "org.example.test.Ping", nil, nil, SendFiles(Files{w})); !errors.Is(err, ErrFileDescriptorsUnsupported) {
		t.Fatalf("Invoke() error = %v, want ErrFileDescriptorsUnsupported", err)
	}
}

// TestOnewayFileDescriptors passes a descriptor with a oneway call.
func TestOnewayFileDescriptors(t *testing.T) {
	received := make(chan []byte, 1)
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Notify", func(ctx context.Context, call UnaryCall) error {
			if !call.IsOneway() {
				t.Error("IsOneway() = false")
			}
			fds := call.Files()
			if len(fds) != 1 {
				return call.ReplyError(ctx, ErrorInvalidParameter, map[string]any{"parameter": "fds"})
			}
			buf := make([]byte, 5)
			if _, err := io.ReadFull(fds[0], buf); err != nil {
				t.Errorf("reading descriptor: %v", err)
			}
			received <- buf
			return nil
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, false)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		_, _ = w.Write([]byte("noise"))
		_ = w.Close()
	}()
	if err := client.Oneway(context.Background(), "org.example.fds.Notify", struct {
		Data string `json:"data"`
	}{Data: "x"}, SendFiles(Files{r})); err != nil {
		t.Fatalf("Oneway() error = %v", err)
	}

	// Unblock the service's read; it wrote the payload above.
	select {
	case buf := <-received:
		if string(buf) != "noise" {
			t.Fatalf("received %q, want %q", buf, "noise")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for oneway delivery")
	}
}

// TestFDConnCoalescedDescriptors verifies that descriptors delivered by the
// kernel with a later message in the same recvmsg are not associated with an
// earlier message that happens to start the chunk.
func TestFDConnCoalescedDescriptors(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	c := &fdConn{}
	c.scanChunk([]byte("first\x00second\x00"), []*os.File{r})

	if files := c.takeMessageFDs(); len(files) != 0 {
		closeFiles(files)
		t.Fatalf("first message carried %d descriptors, want 0", len(files))
	}
	files := c.takeMessageFDs()
	if len(files) != 1 || files[0] != r {
		closeFiles(files)
		t.Fatalf("second message carried %d descriptors, want the passed descriptor", len(files))
	}
	closeFiles(files)
}

// TestStreamFileDescriptors streams replies with and without descriptors
// and verifies that each descriptor is associated with the message it was
// sent with, even when several messages are received in one chunk.
func TestStreamFileDescriptors(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			// First reply without descriptors.
			if err := call.Send(ctx, struct {
				Index int `json:"index"`
			}{Index: 0}); err != nil {
				return err
			}
			// Second reply with a descriptor, sent right after the
			// first one so that both may arrive in one chunk.
			r, w, err := os.Pipe()
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte("payload-1")); err != nil {
				return err
			}
			_ = w.Close()
			if _, err := call.PushFiles(Files{r}); err != nil {
				return err
			}
			if err := call.Send(ctx, struct {
				Index int `json:"index"`
			}{Index: 1}); err != nil {
				return err
			}
			r2, w2, err := os.Pipe()
			if err != nil {
				return err
			}
			if _, err := w2.Write([]byte("payload-2")); err != nil {
				return err
			}
			_ = w2.Close()
			if _, err := call.PushFiles(Files{r2}); err != nil {
				return err
			}
			return call.Close(ctx, struct {
				Index int `json:"index"`
			}{Index: 2})
		}); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, true)
	stream, err := client.Stream(context.Background(), "org.example.fds.Watch", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	var out struct {
		Index int `json:"index"`
	}
	if err := stream.Recv(context.Background(), &out); err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if out.Index != 0 {
		t.Fatalf("first reply index = %d, want 0", out.Index)
	}

	var files Files
	err = stream.Recv(context.Background(), &out, ReceiveFiles(&files))
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if out.Index != 1 {
		t.Fatalf("second reply index = %d, want 1", out.Index)
	}
	if len(files) != 1 {
		t.Fatalf("second reply carried %d descriptors, want 1", len(files))
	}
	payload, err := io.ReadAll(files[0])
	if err != nil {
		t.Fatalf("reading descriptor: %v", err)
	}
	if string(payload) != "payload-1" {
		t.Fatalf("second reply descriptor = %q, want %q", payload, "payload-1")
	}

	files = nil
	err = stream.Recv(context.Background(), &out, ReceiveFiles(&files))
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if out.Index != 2 {
		t.Fatalf("final reply index = %d, want 2", out.Index)
	}
	if len(files) != 1 {
		t.Fatalf("final reply carried %d descriptors, want 1", len(files))
	}
	payload, err = io.ReadAll(files[0])
	if err != nil {
		t.Fatalf("reading descriptor: %v", err)
	}
	if string(payload) != "payload-2" {
		t.Fatalf("final reply descriptor = %q, want %q", payload, "payload-2")
	}

	if err := stream.Recv(context.Background(), &out); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() after end = %v, want io.EOF", err)
	}
}

// TestFileDescriptorsSecondTakeReturnsNil verifies the ownership transfer
// of the call's descriptors: given a handler that calls Files() twice, when
// the second call returns, then it gets nil instead of the same descriptors
// again, so they cannot be closed twice.
func TestFileDescriptorsSecondTakeReturnsNil(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			first := call.Files()
			if len(first) != 1 {
				t.Errorf("first Files() = %d files, want 1", len(first))
			}
			if second := call.Files(); second != nil {
				t.Errorf("second Files() = %v, want nil", second)
				second.Close()
			}
			first.Close()
			return call.Reply(ctx, struct {
				Length int `json:"length"`
			}{Length: 0})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, true)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Length int `json:"length"`
	}
	if err := client.Invoke(context.Background(), "org.example.fds.Echo", struct {
		Data string `json:"data"`
	}{Data: "x"}, &out, SendFiles(Files{w})); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	_ = w.Close()
	_ = r.Close()
	_ = client.Close()
}

// TestFileDescriptorsClosedWhenNotTaken verifies that descriptors a handler
// does not take ownership of are closed when the call finishes.
func TestFileDescriptorsClosedWhenNotTaken(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			// Deliberately do not call FileDescriptors.
			return call.Reply(ctx, struct {
				Length int `json:"length"`
			}{Length: 0})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	before := countOpenFDs(t)
	client := newFDTestClient(t, addr, true)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Length int `json:"length"`
	}
	if err := client.Invoke(context.Background(), "org.example.fds.Echo", struct {
		Data string `json:"data"`
	}{Data: "x"}, &out, SendFiles(Files{w})); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	_ = w.Close()
	_ = r.Close()
	_ = client.Close()

	// The duplicated descriptor must be gone once the call finished.
	deadline := time.Now().Add(5 * time.Second)
	for {
		after := countOpenFDs(t)
		if after <= before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d file descriptors open, want at most %d", after, before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot count file descriptors: %v", err)
	}
	return len(entries)
}

// TestUpgradeFileDescriptorConn verifies that an upgraded connection
// implements FDConn and can pass descriptors for the upgraded protocol.
func TestUpgradeFileDescriptorConn(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Upgrade("TakeOver", func(ctx context.Context, call UpgradeCall) error {
			conn, err := call.Accept(ctx, nil)
			if err != nil {
				return err
			}
			fdc, ok := conn.(FDConn)
			if !ok {
				t.Error("upgraded connection does not implement FDConn")
				return conn.Close()
			}
			go func() {
				buf := make([]byte, 64)
				n, files, err := fdc.ReadMsg(buf)
				if err != nil || len(files) != 1 {
					t.Errorf("ReadMsg() = %d, %d descriptors, err %v", n, len(files), err)
					return
				}
				if _, err := files[0].Write(buf[:n]); err != nil {
					t.Errorf("writing descriptor: %v", err)
				}
				_ = files[0].Close()
			}()
			return nil
		}); err != nil {
			t.Fatalf("Upgrade() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, true)
	conn, err := client.Upgrade(context.Background(), "org.example.fds.TakeOver", nil, nil)
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	fdc, ok := conn.(FDConn)
	if !ok {
		t.Fatal("upgraded client connection does not implement FDConn")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := fdc.WriteMsg([]byte("raw-bytes"), []*os.File{w}); err != nil {
		t.Fatalf("WriteMsg() error = %v", err)
	}
	_ = w.Close()

	payload, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	if string(payload) != "raw-bytes" {
		t.Fatalf("echoed %q, want %q", payload, "raw-bytes")
	}
	_ = conn.Close()
}

// TestFileDescriptorCountMismatch verifies the robustness the
// specification requires of methods that consume file descriptors: a
// different number than expected is answered with
// org.varlink.service.InvalidParameter rather than failing the connection.
func TestFileDescriptorCountMismatch(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			if len(call.Files()) != 2 {
				return call.ReplyError(ctx, ErrorInvalidParameter, map[string]any{"parameter": "fds"})
			}
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, false)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out struct {
		Length int `json:"length"`
	}
	err = client.Invoke(context.Background(), "org.example.fds.Echo", struct {
		Data string `json:"data"`
	}{Data: "x"}, &out, SendFiles(Files{w}))
	_ = w.Close()
	if !IsServiceError(err, ErrorInvalidParameter) {
		t.Fatalf("Invoke() error = %v, want InvalidParameter", err)
	}

	// The connection survives the rejected call.
	if err := client.Invoke(context.Background(), "org.varlink.service.GetInfo", nil, nil); err != nil {
		t.Fatalf("GetInfo() after mismatch error = %v", err)
	}
}

// TestUpgradeFileDescriptors verifies that the upgrade call itself can
// carry file descriptors in both directions.
//
// Given a connection that supports file descriptor passing, when the
// client upgrades while sending a descriptor and the service accepts while
// replying with one, then each side receives the other's descriptor and
// the upgraded connection remains usable.
func TestUpgradeFileDescriptors(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Upgrade("TakeOver", func(ctx context.Context, call UpgradeCall) error {
			if !call.SupportsFileDescriptors() {
				t.Error("SupportsFileDescriptors() = false for AF_UNIX call")
			}
			fds := call.Files()
			if len(fds) != 1 {
				return call.ReplyError(ctx, ErrorInvalidParameter, map[string]any{"parameter": "fds"})
			}
			// Write through the descriptor the client passed with the
			// upgrade call.
			if _, err := fds[0].Write([]byte("takeover")); err != nil {
				return err
			}
			// Reply to the upgrade with a descriptor of our own.
			r, w, err := os.Pipe()
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte("confirmed")); err != nil {
				return err
			}
			defer w.Close()
			if _, err := call.PushFiles(Files{r}); err != nil {
				return err
			}
			conn, err := call.Accept(ctx, nil)
			if err != nil {
				return err
			}
			go func() { _ = conn.Close() }()
			return nil
		}); err != nil {
			t.Fatalf("Upgrade() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, true)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var files Files
	conn, err := client.Upgrade(context.Background(), "org.example.fds.TakeOver", nil, nil, SendFiles(Files{w}), ReceiveFiles(&files))
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	_ = w.Close()
	defer conn.Close()

	// The client's pipe must carry the service's write.
	buf := make([]byte, 8)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("reading client pipe: %v", err)
	}
	if string(buf) != "takeover" {
		t.Fatalf("client pipe = %q, want %q", buf, "takeover")
	}

	// The files returned by Upgrade must carry the service's write.
	if len(files) != 1 {
		t.Fatalf("Upgrade() returned %d file descriptors, want 1", len(files))
	}
	payload, err := io.ReadAll(files[0])
	if err != nil {
		t.Fatalf("reading returned descriptor: %v", err)
	}
	if string(payload) != "confirmed" {
		t.Fatalf("returned descriptor = %q, want %q", payload, "confirmed")
	}
	_ = files[0].Close()
}

// TestReplyErrorFileDescriptors verifies that descriptors attached to an
// error reply are closed by the library instead of being delivered.
//
// Given a service that replies with an error carrying a descriptor, when
// the client invokes the method, then the error is delivered and the
// descriptor is closed, so ReceiveFiles stores nothing.
func TestReplyErrorFileDescriptors(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			r, w, err := os.Pipe()
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte("error-fd")); err != nil {
				return err
			}
			defer w.Close()
			if _, err := call.PushFiles(Files{r}); err != nil {
				return err
			}
			return call.ReplyError(ctx, "org.example.fds.EchoFailed", nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, true)
	var files Files
	err := client.Invoke(context.Background(), "org.example.fds.Echo", struct {
		Data string `json:"data"`
	}{Data: "x"}, nil, ReceiveFiles(&files))

	if !IsServiceError(err, "org.example.fds.EchoFailed") {
		t.Fatalf("Invoke() error = %v, want org.example.fds.EchoFailed", err)
	}
	if files != nil {
		t.Fatalf("ReceiveFiles stored %d file descriptors with the error, want none", len(files))
	}
}

// TestBatchFileDescriptors verifies that batched calls carry file
// descriptors per message, in both directions.
//
// Given a batch of unary calls, when one call sends a descriptor and its
// reply carries one, then the descriptor reaches the service and the
// reply's descriptor is stored by the call's ReceiveFiles option.
func TestBatchFileDescriptors(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{AcceptFileDescriptors: true}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			fds := call.Files()
			if len(fds) != 1 {
				return call.ReplyError(ctx, ErrorInvalidParameter, map[string]any{"parameter": "fds"})
			}
			if _, err := fds[0].Write([]byte("batched")); err != nil {
				return err
			}
			r, w, err := os.Pipe()
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte("reply-fd")); err != nil {
				return err
			}
			defer w.Close()
			if _, err := call.PushFiles(Files{r}); err != nil {
				return err
			}
			return call.Reply(ctx, struct {
				Length int `json:"length"`
			}{Length: 7})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, true)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	batch := client.Batch()
	var files Files
	batch.Invoke("org.example.fds.Echo", struct {
		Data string `json:"data"`
	}{Data: "x"}, nil, SendFiles(Files{w}), ReceiveFiles(&files))
	if err := batch.Send(context.Background()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	_ = w.Close()

	// The client's pipe must carry the service's write.
	buf := make([]byte, 7)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("reading client pipe: %v", err)
	}
	if string(buf) != "batched" {
		t.Fatalf("client pipe = %q, want %q", buf, "batched")
	}

	// The reply's descriptor must be stored by the ReceiveFiles option.
	if len(files) != 1 {
		t.Fatalf("reply carried %d file descriptors, want 1", len(files))
	}
	payload, err := io.ReadAll(files[0])
	if err != nil {
		t.Fatalf("reading reply descriptor: %v", err)
	}
	if string(payload) != "reply-fd" {
		t.Fatalf("reply descriptor = %q, want %q", payload, "reply-fd")
	}
	_ = files[0].Close()
}

// TestServerSupportsFileDescriptors verifies the server-side capability
// query.
//
// Given services on AF_UNIX and TCP transports, when handlers query
// SupportsFileDescriptors, then it reports true only on the AF_UNIX
// connection.
func TestServerSupportsFileDescriptors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	unixAddr := newUnixTestServer(t, ServerConfig{}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			if !call.SupportsFileDescriptors() {
				t.Error("SupportsFileDescriptors() = false for AF_UNIX call")
			}
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})

	builder := NewServerBuilder(ServerConfig{})
	tcpIface := mustInterface(t, builder, fdTestInterface)
	if err := tcpIface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
		if call.SupportsFileDescriptors() {
			t.Error("SupportsFileDescriptors() = true for TCP call")
		}
		return call.Reply(ctx, nil)
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}
	server := builder.Build()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = server.Serve(context.Background(), conn) }()
		}
	}()

	for _, addr := range []string{unixAddr, "tcp:" + ln.Addr().String()} {
		client, err := DialWithConfig(context.Background(), addr, ClientConfig{})
		if err != nil {
			t.Fatalf("DialWithConfig(%s) error = %v", addr, err)
		}
		if err := client.Invoke(context.Background(), "org.example.fds.Echo", struct {
			Data string `json:"data"`
		}{Data: "x"}, nil); err != nil {
			t.Fatalf("Invoke(%s) error = %v", addr, err)
		}
		_ = client.Close()
	}
}

// TestCallOptionNotApplicable verifies that options are rejected on the
// operations they cannot apply to.
//
// Given a client connected to a streaming service, when ReceiveFiles is
// passed to Oneway or the opening Stream call, or SendFiles to
// Stream.Recv, then the call fails with ErrInvalidCallOption.
func TestCallOptionNotApplicable(t *testing.T) {
	addr := newUnixTestServer(t, ServerConfig{}, func(b *ServerBuilder) {
		iface := mustInterface(t, b, fdTestInterface)
		if err := iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			<-ctx.Done()
			return nil
		}); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	})

	client := newFDTestClient(t, addr, false)
	defer client.Close()

	var files Files
	if err := client.Oneway(context.Background(), "org.example.fds.Notify", nil, ReceiveFiles(&files)); !errors.Is(err, ErrInvalidCallOption) {
		t.Fatalf("Oneway(ReceiveFiles) error = %v, want %v", err, ErrInvalidCallOption)
	}
	if _, err := client.Stream(context.Background(), "org.example.fds.Watch", nil, ReceiveFiles(&files)); !errors.Is(err, ErrInvalidCallOption) {
		t.Fatalf("Stream(ReceiveFiles) error = %v, want %v", err, ErrInvalidCallOption)
	}
	stream, err := client.Stream(context.Background(), "org.example.fds.Watch", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if err := stream.Recv(context.Background(), nil, SendFiles(Files{})); !errors.Is(err, ErrInvalidCallOption) {
		t.Fatalf("Recv(SendFiles) error = %v, want %v", err, ErrInvalidCallOption)
	}
	_ = stream.Close()
}
