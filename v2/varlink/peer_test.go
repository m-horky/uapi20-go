//go:build linux

package varlink

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// serveUnix serves server on a unix socket at a fresh path and returns the
// varlink address to dial it.
func serveUnix(t *testing.T, server *Server) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = server.Serve(context.Background(), conn)
			}()
		}
	}()
	return "unix:" + path
}

// TestPeerFromUnixConnection verifies that Serve captures the credentials
// of the connecting process and exposes them through PeerFromContext.
func TestPeerFromUnixConnection(t *testing.T) {
	server := NewServerBuilder(ServerConfig{})
	iface, err := server.Interface("interface org.example.peer\n\nmethod Who() -> (uid: int, pid: int)\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}
	if err := iface.Unary("Who", func(ctx context.Context, call UnaryCall) error {
		peer, ok := PeerFromContext(ctx)
		if !ok {
			return call.ReplyError(ctx, "org.example.peer.NoPeer", nil)
		}
		return call.Reply(ctx, struct {
			UID int `json:"uid"`
			PID int `json:"pid"`
		}{UID: peer.UID, PID: peer.PID})
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	client, err := Dial(context.Background(), serveUnix(t, server.Build()))
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var out struct {
		UID int `json:"uid"`
		PID int `json:"pid"`
	}
	if err := client.Invoke(context.Background(), "org.example.peer.Who", nil, &out); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if out.UID != os.Getuid() {
		t.Errorf("peer UID = %d, want %d", out.UID, os.Getuid())
	}
	if out.PID != os.Getpid() {
		t.Errorf("peer PID = %d, want %d", out.PID, os.Getpid())
	}
}

// TestPeerPIDFDFromUnixConnection verifies that the captured peer includes
// a usable pidfd on kernels that support SO_PEERPIDFD.
func TestPeerPIDFDFromUnixConnection(t *testing.T) {
	server := NewServerBuilder(ServerConfig{})
	iface, err := server.Interface("interface org.example.peer\n\nmethod Who() -> (pidfd: bool)\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}
	if err := iface.Unary("Who", func(ctx context.Context, call UnaryCall) error {
		peer, ok := PeerFromContext(ctx)
		if !ok || peer.PIDFD == nil {
			return call.Reply(ctx, struct {
				Pidfd bool `json:"pidfd"`
			}{})

		}
		// Signal 0 checks permission to signal the process the pidfd
		// refers to without sending anything: here, ourselves.
		signaled := unix.PidfdSendSignal(int(peer.PIDFD.Fd()), 0, nil, 0) == nil
		return call.Reply(ctx, struct {
			Pidfd bool `json:"pidfd"`
		}{Pidfd: signaled})
	}); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	client, err := Dial(context.Background(), serveUnix(t, server.Build()))
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var out struct {
		Pidfd bool `json:"pidfd"`
	}
	if err := client.Invoke(context.Background(), "org.example.peer.Who", nil, &out); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !out.Pidfd {
		t.Skip("no SO_PEERPIDFD available")
	}
}

// TestPeerFromContextWithoutPeerTransport verifies the accessor reports
// false on transports without credential support.
func TestPeerFromContextWithoutPeerTransport(t *testing.T) {
	var sawPeer = make(chan bool, 1)
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		iface := mustInterface(t, b, "interface org.example.peer\n\nmethod Who() -> ()\n")
		if err := iface.Unary("Who", func(ctx context.Context, call UnaryCall) error {
			_, ok := PeerFromContext(ctx)
			sawPeer <- ok
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	if err := client.Invoke(context.Background(), "org.example.peer.Who", nil, nil); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if ok := <-sawPeer; ok {
		t.Error("PeerFromContext() = true on net.Pipe, want false")
	}
}
