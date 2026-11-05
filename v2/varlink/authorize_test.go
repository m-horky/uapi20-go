package varlink

import (
	"context"
	"errors"
	"testing"
)

// TestServerAuthorize verifies per-call authorization: denials reply with
// org.varlink.service.PermissionDenied, and *RemoteError denials are passed
// through.
func TestServerAuthorize(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		b.cfg.Authorize = func(ctx context.Context, method string) error {
			switch method {
			case "org.example.auth.Public":
				return nil
			case "org.example.auth.Custom":
				return &RemoteError{Name: "org.example.auth.Denied"}
			default:
				return errors.New("no")
			}
		}
		iface := mustInterface(t, b, "interface org.example.auth\n\nmethod Public() -> ()\n\nmethod Secret() -> ()\n\nmethod Custom() -> ()\n")
		handler := func(ctx context.Context, call UnaryCall) error {
			return call.Reply(ctx, nil)
		}
		for _, m := range []string{"Public", "Secret", "Custom"} {
			if err := iface.Unary(m, handler); err != nil {
				t.Fatalf("Unary(%s) error = %v", m, err)
			}
		}
	})
	defer closeClientServer(t, client, done)

	ctx := context.Background()
	if err := client.Invoke(ctx, "org.example.auth.Public", nil, nil); err != nil {
		t.Fatalf("Invoke(Public) error = %v", err)
	}
	if err := client.Invoke(ctx, "org.example.auth.Secret", nil, nil); !IsServiceError(err, ErrorPermissionDenied) {
		t.Fatalf("Invoke(Secret) error = %v, want %s", err, ErrorPermissionDenied)
	}
	if err := client.Invoke(ctx, "org.example.auth.Custom", nil, nil); !IsServiceError(err, "org.example.auth.Denied") {
		t.Fatalf("Invoke(Custom) error = %v, want org.example.auth.Denied", err)
	}
}

// TestServerAuthorizeOneway verifies that a denied oneway call produces no
// reply and leaves the connection usable.
func TestServerAuthorizeOneway(t *testing.T) {
	client, done := newTestClientServer(t, func(b *ServerBuilder) {
		b.cfg.Authorize = func(ctx context.Context, method string) error {
			return errors.New("no")
		}
		iface := mustInterface(t, b, "interface org.example.auth\n\nmethod Ping() -> ()\n")
		if err := iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			return call.Reply(ctx, nil)
		}); err != nil {
			t.Fatalf("Unary() error = %v", err)
		}
	})
	defer closeClientServer(t, client, done)

	ctx := context.Background()
	if err := client.Oneway(ctx, "org.example.auth.Ping", nil); err != nil {
		t.Fatalf("Oneway() error = %v", err)
	}
	// The denied oneway call gets no reply; the connection stays usable.
	if err := client.Invoke(ctx, "org.example.auth.Ping", nil, nil); !IsServiceError(err, ErrorPermissionDenied) {
		t.Fatalf("Invoke() error = %v, want %s", err, ErrorPermissionDenied)
	}
}
