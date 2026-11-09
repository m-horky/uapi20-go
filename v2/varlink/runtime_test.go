package varlink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnClientInvoke(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		b.SetInfo(ServiceInfo{
			Vendor:  "Test Vendor",
			Product: "Test Product",
			Version: "1.0",
			URL:     "https://example.test/service",
		})
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping(message: string) -> (message: string)\n")
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
	})
	defer closeClientServer(t, client, done)

	var out struct {
		Message string `json:"message"`
	}
	err := client.Invoke(context.Background(), "org.example.test.Ping", struct {
		Message string `json:"message"`
	}{Message: "hello"}, &out)

	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got, want := out.Message, "pong:hello"; got != want {
		t.Fatalf("Invoke() message = %q, want %q", got, want)
	}
}

func TestConnClientInvokeOmitsEmptyParameters(t *testing.T) {
	var written atomic.Value
	conn := &capturingConn{
		reader: bytes.NewBufferString(`{}` + "\x00"),
	}
	conn.written = &written
	client := NewClient(conn, ClientConfig{})

	if err := client.Invoke(context.Background(), "org.example.test.Ping", struct{}{}, nil); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	frame, _ := written.Load().(string)
	if strings.Contains(frame, "parameters") {
		t.Fatalf("call frame contains parameters: %q", frame)
	}
}

func TestConnClientInvokeDecodeErrorKeepsClientUsable(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping(message: string) -> (message: string)\n")
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
	})
	defer closeClientServer(t, client, done)

	var wrongOut struct {
		Message int `json:"message"`
	}
	err := client.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "bad-shape"}, &wrongOut)
	var replyErr *ReplyError
	if !errors.As(err, &replyErr) {
		t.Fatalf("Invoke() decode error = %T %v, want *ReplyError", err, err)
	}

	var out struct {
		Message string `json:"message"`
	}
	if err := client.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "after"}, &out); err != nil {
		t.Fatalf("Invoke() after decode error = %v", err)
	}
	if out.Message != "pong:after" {
		t.Fatalf("Invoke() after decode error message = %q", out.Message)
	}
}

func TestConnClientServiceInterface(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		b.SetInfo(ServiceInfo{
			Vendor:  "Vendor",
			Product: "Product",
			Version: "2",
			URL:     "https://example.test/v2",
		})
		mustInterface(t, b, "interface org.example.test\n\nmethod Ping() -> ()\n")
	})
	defer closeClientServer(t, client, done)

	var info struct {
		Vendor     string   `json:"vendor"`
		Product    string   `json:"product"`
		Version    string   `json:"version"`
		URL        string   `json:"url"`
		Interfaces []string `json:"interfaces"`
	}
	if err := client.Invoke(context.Background(), "org.varlink.service.GetInfo", nil, &info); err != nil {
		t.Fatalf("GetInfo error = %v", err)
	}
	if info.Vendor != "Vendor" || info.Product != "Product" || info.Version != "2" || info.URL != "https://example.test/v2" {
		t.Fatalf("GetInfo returned wrong metadata: %+v", info)
	}
	if got, want := len(info.Interfaces), 2; got != want {
		t.Fatalf("GetInfo interfaces len = %d, want %d", got, want)
	}
	if info.Interfaces[0] != ServiceInterfaceName || info.Interfaces[1] != "org.example.test" {
		t.Fatalf("GetInfo interfaces = %v", info.Interfaces)
	}

	var desc struct {
		Description string `json:"description"`
	}
	if err := client.Invoke(context.Background(), "org.varlink.service.GetInterfaceDescription", map[string]string{"interface": "org.example.test"}, &desc); err != nil {
		t.Fatalf("GetInterfaceDescription error = %v", err)
	}
	if desc.Description != "interface org.example.test\n\nmethod Ping() -> ()\n" {
		t.Fatalf("GetInterfaceDescription returned %q", desc.Description)
	}

	if err := client.Invoke(context.Background(), "org.varlink.service.GetInterfaceDescription", map[string]string{"interface": ServiceInterfaceName}, &desc); err != nil {
		t.Fatalf("GetInterfaceDescription built-in error = %v", err)
	}
	for _, want := range []string{"ExpectedMore", "ExpectedUpgrade", "PermissionDenied", "GetInfo", "GetInterfaceDescription"} {
		if !strings.Contains(desc.Description, want) {
			t.Fatalf("built-in service description missing %q: %q", want, desc.Description)
		}
	}
	if strings.Contains(desc.Description, "InternalError") {
		t.Fatalf("built-in service description advertises InternalError: %q", desc.Description)
	}

	err := client.Invoke(context.Background(), "org.varlink.service.GetInterfaceDescription", map[string]string{"interface": "org.example.missing"}, &desc)
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("GetInterfaceDescription missing error = %T %v, want *RemoteError", err, err)
	}
	if remote.Name != ErrorInterfaceNotFound {
		t.Fatalf("GetInterfaceDescription missing error name = %q, want %q", remote.Name, ErrorInterfaceNotFound)
	}
	var params struct {
		Interface string `json:"interface"`
	}
	if err := remote.Decode(&params); err != nil {
		t.Fatalf("RemoteError.Decode() error = %v", err)
	}
	if params.Interface != "org.example.missing" {
		t.Fatalf("InterfaceNotFound parameter = %q", params.Interface)
	}
}

func TestConnClientOneway(t *testing.T) {
	var calls atomic.Int32
	doneCall := make(chan struct{}, 1)
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Notify(message: string) -> ()\n")
		if err := iface.Unary("Notify", func(ctx context.Context, call UnaryCall) error {
			if !call.IsOneway() {
				t.Error("expected oneway request")
			}
			calls.Add(1)
			doneCall <- struct{}{}
			return nil
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	if err := client.Oneway(context.Background(), "org.example.test.Notify", map[string]string{"message": "hello"}); err != nil {
		t.Fatalf("Oneway() error = %v", err)
	}
	<-doneCall
	if got := calls.Load(); got != 1 {
		t.Fatalf("oneway handler calls = %d, want 1", got)
	}
}

func TestConnClientStream(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Watch() -> (value: string)\n")
		if err := iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			for _, value := range []string{"one", "two"} {
				if err := call.Send(ctx, struct {
					Value string `json:"value"`
				}{Value: value}); err != nil {
					return err
				}
			}
			return call.Close(ctx, struct {
				Value string `json:"value"`
			}{Value: "three"})
		}); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	stream, err := client.Stream(context.Background(), "org.example.test.Watch", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	want := []string{"one", "two", "three"}
	for _, expected := range want {
		var out struct {
			Value string `json:"value"`
		}
		if err := stream.Recv(context.Background(), &out); err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		if out.Value != expected {
			t.Fatalf("Recv() value = %q, want %q", out.Value, expected)
		}
	}
	if err := stream.Recv(context.Background(), nil); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() final error = %v, want %v", err, io.EOF)
	}
}

func TestConnClientStreamCloseBeforeFinalReplyDoesNotPanic(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Watch() -> (value: string)\n")
		if err := iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			return call.Send(ctx, struct {
				Value string `json:"value"`
			}{Value: "one"})
		}); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	})

	stream, err := client.Stream(context.Background(), "org.example.test.Watch", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Stream.Close() panic = %v", recovered)
		}
	}()
	if err := stream.Close(); err != nil {
		t.Fatalf("Stream.Close() error = %v", err)
	}
	<-done
}

func TestConnClientStreamRecvAfterCloseReturnsErrStreamClosed(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Watch() -> (value: string)\n")
		if err := iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			return call.Send(ctx, struct {
				Value string `json:"value"`
			}{Value: "one"})
		}); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	})

	stream, err := client.Stream(context.Background(), "org.example.test.Watch", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Stream.Close() error = %v", err)
	}
	if err := stream.Recv(context.Background(), nil); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Recv() after Close error = %v, want %v", err, ErrStreamClosed)
	}
	<-done
}

func TestConnClientStreamRecvErrorIsTerminal(t *testing.T) {
	conn := &scriptedConn{
		reader: bytes.NewBufferString(`{"error":"org.example.test.Failed"}` + "\x00"),
	}
	client := NewClient(conn, ClientConfig{})
	stream, err := client.Stream(context.Background(), "org.example.test.Watch", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	err = stream.Recv(context.Background(), nil)
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("Recv() error = %T %v, want *RemoteError", err, err)
	}
	if err := stream.Recv(context.Background(), nil); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() second error = %v, want %v", err, io.EOF)
	}
}

func TestConnClientStreamReplyErrorIsTerminal(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Watch() -> (value: string)\n")
		if err := iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			return call.ReplyError(ctx, "org.example.test.Failed", struct {
				Reason string `json:"reason"`
			}{Reason: "boom"})
		}); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	stream, err := client.Stream(context.Background(), "org.example.test.Watch", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	err = stream.Recv(context.Background(), nil)
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("Recv() error = %T %v, want *RemoteError", err, err)
	}
	if remote.Name != "org.example.test.Failed" {
		t.Fatalf("remote name = %q", remote.Name)
	}
}

func TestConnClientUpgrade(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Bridge() -> (ready: bool)\n")
		if err := iface.Upgrade("Bridge", func(ctx context.Context, call UpgradeCall) error {
			conn, err := call.Accept(ctx, struct {
				Ready bool `json:"ready"`
			}{Ready: true})

			if err != nil {
				return err
			}
			buf := make([]byte, 4)
			if _, err := io.ReadFull(conn, buf); err != nil {
				return err
			}
			_, err = conn.Write([]byte("pong"))
			return err
		}); err != nil {
			t.Fatalf("Upgrade() error = %v", err)
		}
	})
	defer waitServerDone(t, done)

	var out struct {
		Ready bool `json:"ready"`
	}
	upgraded, err := client.Upgrade(context.Background(), "org.example.test.Bridge", nil, &out)
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if !out.Ready {
		t.Fatal("Upgrade() reply did not set Ready")
	}
	defer upgraded.Close()

	if _, err := upgraded.Write([]byte("ping")); err != nil {
		t.Fatalf("upgraded Write() error = %v", err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(upgraded, reply); err != nil {
		t.Fatalf("upgraded Read() error = %v", err)
	}
	if got, want := string(reply), "pong"; got != want {
		t.Fatalf("upgraded reply = %q, want %q", got, want)
	}
}

func TestConnClientUpgradePreservesBufferedBytes(t *testing.T) {
	conn := &scriptedConn{
		reader: bytes.NewBufferString(`{"parameters":{"ready":true}}` + "\x00" + "early"),
	}
	client := NewClient(conn, ClientConfig{})

	var out struct {
		Ready bool `json:"ready"`
	}
	upgraded, err := client.Upgrade(context.Background(), "org.example.test.Bridge", nil, &out)
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if !out.Ready {
		t.Fatal("Upgrade() reply did not set Ready")
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(upgraded, buf); err != nil {
		t.Fatalf("upgraded Read() error = %v", err)
	}
	if got, want := string(buf), "early"; got != want {
		t.Fatalf("upgraded buffered bytes = %q, want %q", got, want)
	}
}

func TestServerUpgradePreservesBufferedBytes(t *testing.T) {
	readEarly := make(chan string, 1)
	builder := NewServerBuilder(ServerConfig{})
	iface, err := builder.Interface("interface org.example.test\n\nmethod Bridge() -> (ready: bool)\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}
	if err := iface.Upgrade("Bridge", func(ctx context.Context, call UpgradeCall) error {
		conn, err := call.Accept(ctx, struct {
			Ready bool `json:"ready"`
		}{Ready: true})

		if err != nil {
			return err
		}
		buf := make([]byte, 5)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return err
		}
		readEarly <- string(buf)
		return nil
	}); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	conn := &scriptedConn{
		reader: bytes.NewBufferString(`{"method":"org.example.test.Bridge","upgrade":true}` + "\x00" + "early"),
	}
	if err := builder.Build().Serve(context.Background(), conn); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got, want := <-readEarly, "early"; got != want {
		t.Fatalf("server upgrade buffered bytes = %q, want %q", got, want)
	}
	if got := conn.writer.String(); !strings.Contains(got, `"ready":true`) {
		t.Fatalf("upgrade reply = %q, want ready parameter", got)
	}
}

func TestServerUpgradeRejectionKeepsConnectionUsable(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Bridge() -> (ready: bool)\n\nmethod Ping(message: string) -> (message: string)\n")
		if err := iface.Upgrade("Bridge", func(ctx context.Context, call UpgradeCall) error {
			return call.ReplyError(ctx, "org.example.test.Failed", struct {
				Reason string `json:"reason"`
			}{Reason: "boom"})
		}); err != nil {
			t.Fatalf("Upgrade() error = %v", err)
		}
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
	})
	defer closeClientServer(t, client, done)

	_, err := client.Upgrade(context.Background(), "org.example.test.Bridge", nil, nil)
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("Upgrade() error = %T %v, want *RemoteError", err, err)
	}
	if remote.Name != "org.example.test.Failed" {
		t.Fatalf("remote name = %q", remote.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out struct {
		Message string `json:"message"`
	}
	if err := client.Invoke(ctx, "org.example.test.Ping", map[string]string{"message": "after"}, &out); err != nil {
		t.Fatalf("Invoke() after rejected upgrade error = %v", err)
	}
	if out.Message != "pong:after" {
		t.Fatalf("Invoke() after rejected upgrade message = %q", out.Message)
	}
}

func TestFramedTransportDetachPreservesNetConnWithBufferedBytes(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	writeDone := make(chan error, 1)
	go func() {
		_, err := clientConn.Write([]byte(`{"method":"org.example.test.Bridge","upgrade":true}` + "\x00" + "early"))
		writeDone <- err
	}()

	transport := newFramedTransport(serverConn, 0, false)
	if _, err := transport.readMessage(context.Background(), 0); err != nil {
		t.Fatalf("readMessage() error = %v", err)
	}
	if buffered := transport.wire.Buffered(); buffered == 0 {
		t.Fatal("test did not buffer upgraded bytes")
	}

	upgraded := transport.detach()
	defer upgraded.Close()
	if _, ok := upgraded.(net.Conn); !ok {
		t.Fatalf("detach() = %T, want net.Conn", upgraded)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(upgraded, buf); err != nil {
		t.Fatalf("upgraded Read() error = %v", err)
	}
	if got, want := string(buf), "early"; got != want {
		t.Fatalf("upgraded buffered bytes = %q, want %q", got, want)
	}

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("client write error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client write did not complete")
	}
}

func TestServerHandlerPanicClosesConnectionWithoutRemoteError(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	iface := mustInterface(t, builder, "interface org.example.test\n\nmethod Panic() -> ()\n")
	if err := iface.Unary("Panic", func(context.Context, UnaryCall) error {
		panic("boom")
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}
	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- builder.Build().Serve(context.Background(), serverConn)
	}()
	client := NewClient(clientConn, ClientConfig{})

	err := client.Invoke(context.Background(), "org.example.test.Panic", nil, nil)
	var remote *RemoteError
	if err == nil {
		t.Fatal("Invoke() unexpectedly succeeded")
	}
	if errors.As(err, &remote) {
		t.Fatalf("Invoke() error = %T %v, want connection close without remote error", err, err)
	}

	serveErr := <-done
	if serveErr == nil || !strings.Contains(serveErr.Error(), "handler panic: boom") {
		t.Fatalf("Serve() error = %v, want handler panic", serveErr)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close() error = %v", err)
	}
}

func TestServerCancelsHandlerOnClientDisconnect(t *testing.T) {
	handlerStarted := make(chan struct{})
	handlerDone := make(chan error, 1)
	builder := NewServerBuilder(ServerConfig{})
	iface := mustInterface(t, builder, "interface org.example.test\n\nmethod Slow() -> ()\n")
	if err := iface.Unary("Slow", func(ctx context.Context, call UnaryCall) error {
		close(handlerStarted)
		<-ctx.Done()
		handlerDone <- ctx.Err()
		return ctx.Err()
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	serverConn, clientConn := net.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- builder.Build().Serve(context.Background(), serverConn)
	}()

	client := NewClient(clientConn, ClientConfig{})
	invokeDone := make(chan error, 1)
	go func() {
		invokeErr := client.Invoke(context.Background(), "org.example.test.Slow", nil, nil)
		invokeDone <- invokeErr
	}()

	<-handlerStarted
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close() error = %v", err)
	}

	select {
	case err := <-handlerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not canceled after client disconnect")
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v, want clean exit on client disconnect", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after client disconnect")
	}
	<-invokeDone
}

func TestServerPipelinesCalls(t *testing.T) {
	// The client sends two calls before reading any reply; the server must
	// answer both, in order.
	builder := NewServerBuilder(ServerConfig{})
	iface := mustInterface(t, builder, "interface org.example.test\n\nmethod Ping(n: int) -> (n: int)\n")
	if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
		var in struct {
			N int `json:"n"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, in)
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	serverConn, clientConn := net.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- builder.Build().Serve(context.Background(), serverConn)
	}()
	client := NewClient(clientConn, ClientConfig{})

	var first, second struct {
		N int `json:"n"`
	}
	batch := client.Batch()
	batch.Invoke("org.example.test.Ping", map[string]int{"n": 1}, &first)
	batch.Invoke("org.example.test.Ping", map[string]int{"n": 2}, &second)
	if err := batch.Send(context.Background()); err != nil {
		t.Fatalf("Batch.Send(ctx) error = %v", err)
	}
	if first.N != 1 || second.N != 2 {
		t.Fatalf("replies = %d, %d, want 1, 2", first.N, second.N)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close() error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestTransportReadMessageContextCancelUnblocks(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newFramedTransport(serverConn, 0, false).readMessage(ctx, 0)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("readMessage() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("readMessage() did not unblock after context cancellation")
	}
}

func TestTransportWriteMessageContextCancelUnblocks(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := newFramedTransport(serverConn, 0, false).writeMessage(ctx, 0, Message{
			Method: "org.example.test.Ping",
		})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("writeMessage() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("writeMessage() did not unblock after context cancellation")
	}
}

func TestConnClientInvokeContextCancelClosesClient(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, serverConn)
		close(copyDone)
	}()

	client := NewClient(clientConn, ClientConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		invokeErr := client.Invoke(ctx, "org.example.test.Ping", nil, nil)
		done <- invokeErr
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Invoke() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("Invoke() did not unblock after context cancellation")
	}
	if err := client.Invoke(context.Background(), "org.example.test.Ping", nil, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Invoke() after cancel error = %v, want %v", err, io.ErrClosedPipe)
	}
	<-copyDone
}

func TestConnClientOnewayContextCancelClosesClient(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	client := NewClient(clientConn, ClientConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.Oneway(ctx, "org.example.test.Notify", nil)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Oneway() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("Oneway() did not unblock after context cancellation")
	}
	if err := client.Oneway(context.Background(), "org.example.test.Notify", nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Oneway() after cancel error = %v, want %v", err, io.ErrClosedPipe)
	}
}

func TestConnClientBatchInvoke(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping(message: string) -> (message: string)\n\nmethod Notify(message: string) -> ()\n")
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
		if err := iface.Unary("Notify", func(ctx context.Context, call UnaryCall) error {
			if !call.IsOneway() {
				t.Error("expected oneway request")
			}
			return nil
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	var first struct {
		Message string `json:"message"`
	}
	var second struct {
		Message string `json:"message"`
	}
	batch := client.Batch()
	batch.Invoke("org.example.test.Ping", map[string]string{"message": "one"}, &first)
	batch.Oneway("org.example.test.Notify", map[string]string{"message": "side"})
	batch.Invoke("org.example.test.Ping", map[string]string{"message": "two"}, &second)
	if err := batch.Send(context.Background()); err != nil {
		t.Fatalf("Batch.Send(ctx) error = %v", err)
	}
	if first.Message != "pong:one" {
		t.Fatalf("first reply = %q", first.Message)
	}
	if second.Message != "pong:two" {
		t.Fatalf("second reply = %q", second.Message)
	}
	if err := batch.Send(context.Background()); !errors.Is(err, ErrBatchSent) {
		t.Fatalf("Batch.Send(ctx) second error = %v, want %v", err, ErrBatchSent)
	}
}

func TestConnClientBatchRemoteErrorClosesClient(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping(message: string) -> (message: string)\n")
		if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			var in struct {
				Message string `json:"message"`
			}
			if err := call.Decode(&in); err != nil {
				return err
			}
			if in.Message == "fail" {
				return call.ReplyError(ctx, "org.example.test.Failed", struct {
					Reason string `json:"reason"`
				}{Reason: "boom"})

			}
			return call.Reply(ctx, struct {
				Message string `json:"message"`
			}{Message: "pong:" + in.Message})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer waitServerDone(t, done)

	var first struct {
		Message string `json:"message"`
	}
	var second struct {
		Message string `json:"message"`
	}
	batch := client.Batch()
	batch.Invoke("org.example.test.Ping", map[string]string{"message": "one"}, &first)
	batch.Invoke("org.example.test.Ping", map[string]string{"message": "fail"}, &second)
	err := batch.Send(context.Background())
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("Batch.Send(ctx) error = %T %v, want *RemoteError", err, err)
	}
	if remote.Name != "org.example.test.Failed" {
		t.Fatalf("remote name = %q", remote.Name)
	}
	if first.Message != "pong:one" {
		t.Fatalf("first reply = %q", first.Message)
	}
	if err := client.Invoke(context.Background(), "org.example.test.Ping", map[string]string{"message": "after"}, &second); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Invoke() after batch error = %v, want %v", err, io.ErrClosedPipe)
	}
}

func TestConnClientBatchInvalidRequestReturnsPromptly(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {})
	defer closeClientServer(t, client, done)

	batch := client.Batch()
	batch.Invoke("", nil, nil)
	if err := batch.Send(context.Background()); !errors.Is(err, ErrInvalidMethod) {
		t.Fatalf("Batch.Send(ctx) error = %v, want %v", err, ErrInvalidMethod)
	}
}

func TestConnClientBatchMarshalErrorReturnsPromptly(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {})
	defer closeClientServer(t, client, done)

	batch := client.Batch()
	batch.Invoke("org.example.test.Ping", map[string]any{"bad": make(chan int)}, nil)
	if err := batch.Send(context.Background()); err == nil {
		t.Fatal("Batch.Send(ctx) unexpectedly succeeded")
	}
}

func TestConnClientRemoteError(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nerror Failed (reason: string)\n\nmethod Fail() -> ()\n")
		if err := iface.Unary("Fail", func(ctx context.Context, call UnaryCall) error {
			return call.ReplyError(ctx, "org.example.test.Failed", struct {
				Reason string `json:"reason"`
			}{Reason: "boom"})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	err := client.Invoke(context.Background(), "org.example.test.Fail", nil, nil)
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("Invoke() error = %T %v, want *RemoteError", err, err)
	}
	if remote.Name != "org.example.test.Failed" {
		t.Fatalf("RemoteError.Name = %q", remote.Name)
	}
	var params struct {
		Reason string `json:"reason"`
	}
	if err := remote.Decode(&params); err != nil {
		t.Fatalf("RemoteError.Decode() error = %v", err)
	}
	if params.Reason != "boom" {
		t.Fatalf("RemoteError.Decode() reason = %q", params.Reason)
	}
	if !IsServiceError(err, "org.example.test.Failed") {
		t.Fatal("IsServiceError() = false, want true")
	}
	if IsServiceError(err, "org.example.test.Other") {
		t.Fatal("IsServiceError() = true, want false")
	}
}

func TestConnClientServiceErrors(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		mustInterface(t, b, "interface org.example.test\n\nmethod Ping() -> ()\n")
	})
	defer closeClientServer(t, client, done)

	err := client.Invoke(context.Background(), "org.example.missing.Ping", nil, nil)
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("Invoke() missing interface error = %T %v, want *RemoteError", err, err)
	}
	if remote.Name != ErrorInterfaceNotFound {
		t.Fatalf("missing interface error name = %q", remote.Name)
	}

	err = client.Invoke(context.Background(), "missing", nil, nil)
	if !errors.As(err, &remote) {
		t.Fatalf("Invoke() invalid method error = %T %v, want *RemoteError", err, err)
	}
	if remote.Name != ErrorInvalidParameter {
		t.Fatalf("invalid method error name = %q", remote.Name)
	}
}

func TestServerMethodErrorKinds(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		// Declared but not implemented, plus a declared and implemented one.
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping() -> ()\n\nmethod Todo() -> ()\n")
		if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	tests := []struct {
		method    string
		errorName string
		parameter string
	}{
		{"org.example.test.Undeclared", ErrorMethodNotFound, "org.example.test.Undeclared"},
		{"org.example.test.Todo", ErrorMethodNotImplemented, "org.example.test.Todo"},
		{"org.example.missing.Ping", ErrorInterfaceNotFound, "org.example.missing"},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			err := client.Invoke(context.Background(), tt.method, nil, nil)
			var remote *RemoteError
			if !errors.As(err, &remote) {
				t.Fatalf("Invoke() error = %T %v, want *RemoteError", err, err)
			}
			if remote.Name != tt.errorName {
				t.Fatalf("error name = %q, want %q", remote.Name, tt.errorName)
			}
			var params struct {
				Parameter string `json:"parameter"`
				Interface string `json:"interface"`
				Method    string `json:"method"`
			}
			if err := remote.Decode(&params); err != nil {
				t.Fatalf("RemoteError.Decode() error = %v", err)
			}
			got := params.Parameter + params.Interface + params.Method
			if got != tt.parameter {
				t.Fatalf("error parameter = %q, want %q", got, tt.parameter)
			}
		})
	}
}

func TestServerExpectedMoreAndExpectedUpgrade(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Watch() -> (value: string)\n\nmethod Bridge() -> ()\n")
		if err := iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			return call.Close(ctx, nil)
		}); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
		if err := iface.Upgrade("Bridge", func(ctx context.Context, call UpgradeCall) error {
			_, err := call.Accept(ctx, nil)
			return err
		}); err != nil {
			t.Fatalf("Upgrade() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	err := client.Invoke(context.Background(), "org.example.test.Watch", nil, nil)
	if !IsServiceError(err, ErrorExpectedMore) {
		t.Fatalf("Invoke() on stream method error = %v, want %s", err, ErrorExpectedMore)
	}

	err = client.Invoke(context.Background(), "org.example.test.Bridge", nil, nil)
	if !IsServiceError(err, ErrorExpectedUpgrade) {
		t.Fatalf("Invoke() on upgrade method error = %v, want %s", err, ErrorExpectedUpgrade)
	}
}

func TestServerUnaryRepliesOnceToMoreCall(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping() -> (value: string)\n")
		if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			return call.Reply(ctx, struct {
				Value string `json:"value"`
			}{Value: "once"})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	stream, err := client.Stream(context.Background(), "org.example.test.Ping", nil)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := stream.Recv(context.Background(), &out); err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if out.Value != "once" {
		t.Fatalf("Recv() value = %q", out.Value)
	}
	if err := stream.Recv(context.Background(), nil); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() final error = %v, want %v", err, io.EOF)
	}
}

func TestServerRejectsInvalidParameters(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, `interface org.example.test

type Point (x: int, y: int)

method Add(a: int, b: int) -> (sum: int)

method Move(point: Point, label: ?string) -> ()
`)
		if err := iface.Unary("Add", func(ctx context.Context, call UnaryCall) error {
			var in struct {
				A int `json:"a"`
				B int `json:"b"`
			}
			if err := call.Decode(&in); err != nil {
				return err
			}
			return call.Reply(ctx, struct {
				Sum int `json:"sum"`
			}{Sum: in.A + in.B})
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
		if err := iface.Unary("Move", func(ctx context.Context, call UnaryCall) error {
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	tests := []struct {
		name   string
		method string
		params any
		field  string
	}{
		{"unknown field", "Add", map[string]any{"a": 1, "b": 2, "c": 3}, "c"},
		{"missing field", "Add", map[string]any{"a": 1}, "b"},
		{"wrong type", "Add", map[string]any{"a": 1, "b": "2"}, "b"},
		{"int with fraction", "Add", map[string]any{"a": 1, "b": 2.5}, "b"},
		{"nested unknown field", "Move", map[string]any{"point": map[string]any{"x": 1, "y": 2, "z": 3}}, "point.z"},
		{"nested missing field", "Move", map[string]any{"point": map[string]any{"x": 1}}, "point.y"},
		{"absent parameters", "Add", nil, "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.Invoke(context.Background(), "org.example.test."+tt.method, tt.params, nil)
			if !IsServiceError(err, ErrorInvalidParameter) {
				t.Fatalf("Invoke() error = %v, want %s", err, ErrorInvalidParameter)
			}
			var remote *RemoteError
			errors.As(err, &remote)
			var params struct {
				Parameter string `json:"parameter"`
			}
			if err := remote.Decode(&params); err != nil {
				t.Fatalf("RemoteError.Decode() error = %v", err)
			}
			if params.Parameter != tt.field {
				t.Fatalf("InvalidParameter parameter = %q, want %q", params.Parameter, tt.field)
			}
		})
	}

	// Nullable fields may be omitted; empty collections are not null.
	if err := client.Invoke(context.Background(), "org.example.test.Move", map[string]any{
		"point": map[string]any{"x": 1, "y": 2},
	}, nil); err != nil {
		t.Fatalf("Invoke() with omitted nullable field error = %v", err)
	}
}

func TestServerAcceptsEquivalentParameterEncodings(t *testing.T) {
	var seen atomic.Value
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping() -> (count: int)\n")
		if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			var in struct {
				Count int `json:"count"`
			}
			// Absent, null and {} are all equivalent to no parameters.
			if err := call.Decode(&in); err != nil {
				return err
			}
			seen.Store(in.Count)
			return call.Reply(ctx, in)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	for _, params := range []any{nil, map[string]int{}} {
		if err := client.Invoke(context.Background(), "org.example.test.Ping", params, nil); err != nil {
			t.Fatalf("Invoke(%#v) error = %v", params, err)
		}
		if count, _ := seen.Load().(int); count != 0 {
			t.Fatalf("Invoke(%#v) decoded count = %d, want 0", params, count)
		}
	}
}

func TestServerClosesConnectionOnMalformedMessages(t *testing.T) {
	// Messages that violate the wire protocol have no well-defined reply;
	// the server must close the connection instead.
	tests := []struct {
		name  string
		frame string
	}{
		{"unknown member", `{"method":"org.example.test.Ping","extra":1}`},
		{"oneway and more", `{"method":"org.example.test.Ping","oneway":true,"more":true}`},
		{"more and upgrade", `{"method":"org.example.test.Ping","more":true,"upgrade":true}`},
		{"non-boolean flag", `{"method":"org.example.test.Ping","more":"yes"}`},
		{"missing method", `{"parameters":{}}`},
		{"non-object parameters", `{"method":"org.example.test.Ping","parameters":[1]}`},
		{"reply from client", `{"error":"org.example.test.Failed"}`},
		{"invalid JSON", `{"method":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewServerBuilder(ServerConfig{})
			mustInterface(t, builder, "interface org.example.test\n\nmethod Ping() -> ()\n")

			conn := &scriptedConn{
				reader: bytes.NewBufferString(tt.frame + "\x00"),
			}
			err := builder.Build().Serve(context.Background(), conn)
			if !errors.Is(err, ErrMalformedMessage) && !errors.Is(err, ErrProtocolViolation) {
				t.Fatalf("Serve() error = %v, want malformed message or protocol violation", err)
			}
			if got := conn.writer.String(); got != "" {
				t.Fatalf("Serve() wrote %q, want no reply", got)
			}
			if !conn.closed {
				t.Fatal("connection was not closed")
			}
		})
	}
}

func TestServerIgnoresVendorExtensions(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	iface := mustInterface(t, builder, "interface org.example.test\n\nmethod Ping() -> ()\n")
	if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
		return call.Reply(ctx, nil)
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	conn := &scriptedConn{
		reader: bytes.NewBufferString(`{"method":"org.example.test.Ping","org.example.trace":{"id":7}}` + "\x00"),
	}
	if err := builder.Build().Serve(context.Background(), conn); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := conn.writer.String(); got != "{}\x00" {
		t.Fatalf("reply = %q, want empty reply", got)
	}
}

func TestServerOnewaySuppressesErrorReplies(t *testing.T) {
	var onewayCalls atomic.Int32
	builder := NewServerBuilder(ServerConfig{})
	iface := mustInterface(t, builder, "interface org.example.test\n\nmethod Ping() -> ()\n")
	if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
		if call.IsOneway() {
			onewayCalls.Add(1)
		}
		return call.ReplyError(ctx, "org.example.test.Failed", nil)
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	conn := &scriptedConn{
		reader: bytes.NewBufferString(`{"method":"org.example.test.Ping","oneway":true}` + "\x00" +
			`{"method":"org.example.test.Ping"}` + "\x00"),
	}
	if err := builder.Build().Serve(context.Background(), conn); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	// Only the second, non-oneway call produces a reply.
	if got, want := conn.writer.String(), `{"error":"org.example.test.Failed"}`+"\x00"; got != want {
		t.Fatalf("replies = %q, want %q", got, want)
	}
	if got := onewayCalls.Load(); got != 1 {
		t.Fatalf("oneway handler calls = %d, want 1", got)
	}
}

func TestConnClientRejectsMalformedReplies(t *testing.T) {
	conn := &scriptedConn{
		reader: bytes.NewBufferString(`{"parameters":{},"unknown":1}` + "\x00"),
	}
	client := NewClient(conn, ClientConfig{})

	err := client.Invoke(context.Background(), "org.example.test.Ping", nil, nil)
	var replyErr *ReplyError
	if !errors.As(err, &replyErr) {
		t.Fatalf("Invoke() error = %T %v, want *ReplyError", err, err)
	}
	if !errors.Is(err, ErrMalformedMessage) {
		t.Fatalf("Invoke() error = %v, want ErrMalformedMessage", err)
	}
	if err := client.Invoke(context.Background(), "org.example.test.Ping", nil, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Invoke() after malformed reply error = %v, want %v", err, io.ErrClosedPipe)
	}
}

func TestConnClientRejectsErrorReplyWithContinues(t *testing.T) {
	conn := &scriptedConn{
		reader: bytes.NewBufferString(`{"error":"org.example.test.Failed","continues":true}` + "\x00"),
	}
	client := NewClient(conn, ClientConfig{})

	err := client.Invoke(context.Background(), "org.example.test.Ping", nil, nil)
	var replyErr *ReplyError
	if !errors.As(err, &replyErr) {
		t.Fatalf("Invoke() error = %T %v, want *ReplyError", err, err)
	}
	if !errors.Is(err, ErrMalformedMessage) {
		t.Fatalf("Invoke() error = %v, want ErrProtocolViolation", err)
	}
	if err := client.Invoke(context.Background(), "org.example.test.Ping", nil, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Invoke() after protocol violation error = %v, want %v", err, io.ErrClosedPipe)
	}
}

func TestConnClientIntrospectionHelpers(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		b.SetInfo(ServiceInfo{
			Vendor:  "Example Inc.",
			Product: "Example",
			Version: "1.2.3",
			URL:     "https://example.com",
		})
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping() -> ()\n")
		if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	info, ifaces, err := client.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo() error = %v", err)
	}
	wantInfo := ServiceInfo{Vendor: "Example Inc.", Product: "Example", Version: "1.2.3", URL: "https://example.com"}
	if info != wantInfo {
		t.Fatalf("GetInfo() info = %+v, want %+v", info, wantInfo)
	}
	if !slices.Contains(ifaces, "org.example.test") || !slices.Contains(ifaces, "org.varlink.service") || len(ifaces) != 2 {
		t.Fatalf("GetInfo() interfaces = %v, want [org.example.test org.varlink.service]", ifaces)
	}

	desc, err := client.GetInterfaceDescription(context.Background(), "org.example.test")
	if err != nil {
		t.Fatalf("GetInterfaceDescription() error = %v", err)
	}
	if !strings.Contains(desc, "method Ping() -> ()") {
		t.Fatalf("GetInterfaceDescription() = %q, want it to contain the Ping method", desc)
	}

	_, err = client.GetInterfaceDescription(context.Background(), "org.example.missing")
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Name != ErrorInterfaceNotFound {
		t.Fatalf("GetInterfaceDescription() error = %T %v, want *RemoteError %s", err, err, ErrorInterfaceNotFound)
	}
}

func TestConnClientAnyRoundTripPreservesJSONShape(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.types\n\nmethod Echo(value: any) -> (value: any)\n")
		if err := iface.Unary("Echo", func(ctx context.Context, call UnaryCall) error {
			var in struct {
				Value any `json:"value"`
			}
			if err := call.Decode(&in); err != nil {
				return err
			}
			return call.Reply(ctx, in)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	in := struct {
		Value any `json:"value"`
	}{
		Value: map[string]any{
			"bool":   true,
			"int":    float64(7),
			"float":  3.5,
			"string": "hello",
			"array":  []any{"x", float64(1)},
			"object": map[string]any{"k": "v"},
		},
	}
	var out struct {
		Value any `json:"value"`
	}
	if err := client.Invoke(context.Background(), "org.example.types.Echo", in, &out); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("any round trip mismatch:\n got: %#v\nwant: %#v", out, in)
	}
}

func newTestClientServer(t *testing.T, register func(*ServerBuilder)) (*ConnClient, <-chan error) {
	t.Helper()

	builder := NewServerBuilder(ServerConfig{})
	register(builder)
	server := builder.Build()

	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background(), serverConn)
	}()

	return NewClient(clientConn, ClientConfig{}), done
}

func closeClientServer(t *testing.T, client *ConnClient, done <-chan error) {
	t.Helper()
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close() error = %v", err)
	}
	waitServerDone(t, done)
}

func waitServerDone(t *testing.T, done <-chan error) {
	t.Helper()
	if err := <-done; err != nil {
		t.Fatalf("server.Serve() error = %v", err)
	}
}

func mustInterface(t *testing.T, b *ServerBuilder, description string) *Interface {
	t.Helper()
	iface, err := b.Interface(description)
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}
	return iface
}

type scriptedConn struct {
	reader *bytes.Buffer
	writer bytes.Buffer
	closed bool
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	return c.reader.Read(p)
}

func (c *scriptedConn) Write(p []byte) (int, error) {
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	return c.writer.Write(p)
}

func (c *scriptedConn) Close() error {
	c.closed = true
	return nil
}

type capturingConn struct {
	reader  *bytes.Buffer
	writer  bytes.Buffer
	written *atomic.Value
}

func (c *capturingConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *capturingConn) Write(p []byte) (int, error) {
	c.written.Store(c.writer.String() + string(p))
	return c.writer.Write(p)
}

func (c *capturingConn) Close() error {
	return nil
}

// TestServerUpgradeDoesNotCloseConnection is a regression test: the
// connection handed to an upgrade handler must stay open after Serve
// returns.
func TestServerUpgradeDoesNotCloseConnection(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod TakeOver() -> ()\n")
		if err := iface.Upgrade("TakeOver", func(ctx context.Context, call UpgradeCall) error {
			conn, err := call.Accept(ctx, nil)
			if err != nil {
				return err
			}
			go func() {
				buf := make([]byte, 8)
				n, err := conn.Read(buf)
				if err != nil {
					t.Errorf("upgraded connection read: %v", err)
					return
				}
				if _, err := conn.Write(buf[:n]); err != nil {
					t.Errorf("upgraded connection write: %v", err)
				}
			}()
			return nil
		}); err != nil {
			t.Fatalf("Upgrade() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	conn, err := client.Upgrade(context.Background(), "org.example.test.TakeOver", nil, nil)
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	// Give Serve time to return (and, before the fix, to close the
	// connection out from under the handler).
	time.Sleep(200 * time.Millisecond)
	if _, err := conn.Write([]byte("alive-08")); err != nil {
		t.Fatalf("write after upgrade: %v", err)
	}
	type result struct {
		buf []byte
		err error
	}
	echo := make(chan result, 1)
	go func() {
		buf := make([]byte, 8)
		_, err := io.ReadFull(conn, buf)
		echo <- result{buf, err}
	}()
	select {
	case res := <-echo:
		if res.err != nil {
			t.Fatalf("read after upgrade: %v", res.err)
		}
		if string(res.buf) != "alive-08" {
			t.Fatalf("echo = %q, want %q", res.buf, "alive-08")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading echo after upgrade")
	}
	_ = conn.Close()
}

// TestConnClientOnewayUnknownMethodKeepsReplyOrdering verifies that a
// oneway call to an unknown method consumes no reply, so the client can
// associate the next reply with the next call.
func TestConnClientOnewayUnknownMethodKeepsReplyOrdering(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.test\n\nmethod Ping() -> ()\n")
		if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	// The server cannot reply to the oneway call, not even with
	// MethodNotFound.
	if err := client.Oneway(context.Background(), "org.example.test.Nope", nil); err != nil {
		t.Fatalf("Oneway() error = %v", err)
	}
	// The reply of the following call must not be confused with a reply
	// to the oneway call.
	if err := client.Invoke(context.Background(), "org.example.test.Ping", nil, nil); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
}

func TestServerHandlerPanicIsLogged(t *testing.T) {
	var logged strings.Builder
	logger := funcLogger(func(format string, v ...any) {
		fmt.Fprintf(&logged, format, v...)
	})
	builder := NewServerBuilder(ServerConfig{Logger: logger})
	iface := mustInterface(t, builder, "interface org.example.test\n\nmethod Panic() -> ()\n")
	if err := iface.Unary("Panic", func(context.Context, UnaryCall) error {
		panic("boom")
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}
	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- builder.Build().Serve(context.Background(), serverConn)
	}()
	client := NewClient(clientConn, ClientConfig{})
	_ = client.Invoke(context.Background(), "org.example.test.Panic", nil, nil)
	if err := <-done; err == nil {
		t.Fatal("Serve() error = nil, want handler panic")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close() error = %v", err)
	}
	if out := logged.String(); !strings.Contains(out, "handler panic: boom") || !strings.Contains(out, "goroutine") {
		t.Fatalf("logger output missing panic or stack trace:\n%s", out)
	}
}

// funcLogger adapts a function to the Logger interface.
type funcLogger func(format string, v ...any)

func (f funcLogger) Printf(format string, v ...any) {
	f(format, v...)
}
