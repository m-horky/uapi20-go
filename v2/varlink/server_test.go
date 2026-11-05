package varlink

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func unaryNoop(context.Context, UnaryCall) error   { return nil }
func streamNoop(context.Context, StreamCall) error { return nil }
func upgradeNoop(context.Context, UpgradeCall) error {
	return nil
}

func TestServerBuilderInterface(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})

	iface, err := builder.Interface("interface org.example.test\n\nmethod Ping() -> ()\n\nmethod Watch() -> (value: string)\n\nmethod Bridge() -> ()\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}
	if iface.Name() != "org.example.test" {
		t.Fatalf("Interface name = %q", iface.Name())
	}
	if err := iface.Unary("Ping", unaryNoop); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}
	if err := iface.Stream("Watch", streamNoop); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if err := iface.Upgrade("Bridge", upgradeNoop); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	server := builder.Build()
	got, ok := server.Interface("org.example.test")
	if !ok {
		t.Fatal("server.Interface() not found")
	}
	if _, ok := got.unary["Ping"]; !ok {
		t.Fatal("unary handler Ping not registered")
	}
	if _, ok := got.stream["Watch"]; !ok {
		t.Fatal("stream handler Watch not registered")
	}
	if _, ok := got.upgrade["Bridge"]; !ok {
		t.Fatal("upgrade handler Bridge not registered")
	}

	// The builtin service interface is registered and implemented.
	svc, ok := server.Interface(ServiceInterfaceName)
	if !ok {
		t.Fatal("builtin service interface not registered")
	}
	if _, ok := svc.unary["GetInfo"]; !ok {
		t.Fatal("GetInfo not implemented")
	}
	if _, ok := svc.unary["GetInterfaceDescription"]; !ok {
		t.Fatal("GetInterfaceDescription not implemented")
	}
}

func TestServerBuilderRejectsInvalidDescriptions(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{"empty", ""},
		{"missing interface keyword", "method Ping() -> ()"},
		{"syntax error", "interface org.example.test\n\nmethod Ping ()\n"},
		{"duplicate members", "interface org.example.test\n\nmethod Ping() -> ()\n\nerror Ping ()\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewServerBuilder(ServerConfig{})
			if _, err := builder.Interface(tt.description); err == nil {
				t.Fatal("Interface() unexpectedly succeeded")
			}
		})
	}
}

func TestServerBuilderRejectsDuplicateInterface(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	description := "interface org.example.test\n\nmethod Ping() -> ()\n"
	if _, err := builder.Interface(description); err != nil {
		t.Fatalf("Interface() error = %v", err)
	}
	if _, err := builder.Interface(description); !errors.Is(err, ErrDuplicateInterface) {
		t.Fatalf("Interface() duplicate error = %v, want %v", err, ErrDuplicateInterface)
	}
	if _, err := builder.Interface("interface " + ServiceInterfaceName + "\n\nmethod Ping() -> ()\n"); !errors.Is(err, ErrDuplicateInterface) {
		t.Fatalf("Interface() builtin duplicate error = %v, want %v", err, ErrDuplicateInterface)
	}
}

func TestServerBuilderRejectsDuplicateMethods(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	iface, err := builder.Interface("interface org.example.test\n\nmethod Ping() -> ()\n\nmethod Watch() -> ()\n\nmethod Bridge() -> ()\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}

	if err := iface.Unary("Ping", unaryNoop); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}
	if err := iface.Unary("Ping", unaryNoop); !errors.Is(err, ErrDuplicateMethod) {
		t.Fatalf("Unary() duplicate error = %v, want %v", err, ErrDuplicateMethod)
	}
	if err := iface.Stream("Ping", streamNoop); !errors.Is(err, ErrDuplicateMethod) {
		t.Fatalf("Stream() duplicate error = %v, want %v", err, ErrDuplicateMethod)
	}
	if err := iface.Upgrade("Ping", upgradeNoop); !errors.Is(err, ErrDuplicateMethod) {
		t.Fatalf("Upgrade() duplicate error = %v, want %v", err, ErrDuplicateMethod)
	}
	if err := iface.Stream("Watch", streamNoop); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if err := iface.Upgrade("Watch", upgradeNoop); !errors.Is(err, ErrDuplicateMethod) {
		t.Fatalf("Upgrade() across kinds error = %v, want %v", err, ErrDuplicateMethod)
	}
}

func TestServerBuilderRejectsUndeclaredMethods(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	iface, err := builder.Interface("interface org.example.test\n\nmethod Ping() -> ()\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}

	if err := iface.Unary("Pong", unaryNoop); !errors.Is(err, ErrMethodNotDeclared) {
		t.Fatalf("Unary() undeclared error = %v, want %v", err, ErrMethodNotDeclared)
	}
	if err := iface.Stream("Pong", streamNoop); !errors.Is(err, ErrMethodNotDeclared) {
		t.Fatalf("Stream() undeclared error = %v, want %v", err, ErrMethodNotDeclared)
	}
	if err := iface.Upgrade("Pong", upgradeNoop); !errors.Is(err, ErrMethodNotDeclared) {
		t.Fatalf("Upgrade() undeclared error = %v, want %v", err, ErrMethodNotDeclared)
	}
	if err := iface.Unary("", unaryNoop); !errors.Is(err, ErrInvalidMethod) {
		t.Fatalf("Unary() empty name error = %v, want %v", err, ErrInvalidMethod)
	}
	if err := iface.Unary("org.example.Ping", unaryNoop); !errors.Is(err, ErrInvalidMethod) {
		t.Fatalf("Unary() qualified name error = %v, want %v", err, ErrInvalidMethod)
	}
}

func TestServerBuilderRejectsNilHandlers(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	iface, err := builder.Interface("interface org.example.test\n\nmethod Ping() -> ()\n\nmethod Watch() -> ()\n\nmethod Bridge() -> ()\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}

	if err := iface.Unary("Ping", nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("Unary() nil error = %v, want %v", err, ErrNilHandler)
	}
	if err := iface.Stream("Watch", nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("Stream() nil error = %v, want %v", err, ErrNilHandler)
	}
	if err := iface.Upgrade("Bridge", nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("Upgrade() nil error = %v, want %v", err, ErrNilHandler)
	}
}

func TestServerBuilderBuildIsImmutable(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	iface, err := builder.Interface("interface org.example.test\n\nmethod Ping() -> ()\n\nmethod Later() -> ()\n")
	if err != nil {
		t.Fatalf("Interface() error = %v", err)
	}
	if err := iface.Unary("Ping", unaryNoop); err != nil {
		t.Fatalf("Unary() error = %v", err)
	}

	server := builder.Build()

	// Mutating the builder or the registration handle must not affect the
	// built server.
	if err := iface.Unary("Later", unaryNoop); err != nil {
		t.Fatalf("Unary() after Build error = %v", err)
	}
	got, ok := server.Interface("org.example.test")
	if !ok {
		t.Fatal("interface missing from built server")
	}
	if _, ok := got.unary["Later"]; ok {
		t.Fatal("built server observed post-Build registration")
	}

	if _, err := builder.Interface("interface org.example.other\n\nmethod Ping() -> ()\n"); err != nil {
		t.Fatalf("Interface() after Build error = %v", err)
	}
	if _, ok := server.Interface("org.example.other"); ok {
		t.Fatal("built server observed post-Build interface registration")
	}
}

func TestServerGetInfoListsServiceInterfaceFirst(t *testing.T) {
	builder := NewServerBuilder(ServerConfig{})
	builder.SetInfo(ServiceInfo{Vendor: "v", Product: "p", Version: "1", URL: "u"})
	mustInterface(t, builder, "interface org.example.a\n\nmethod Ping() -> ()\n")
	mustInterface(t, builder, "interface org.example.b\n\nmethod Ping() -> ()\n")

	names := builder.Build().interfaceNames()
	want := []string{ServiceInterfaceName, "org.example.a", "org.example.b"}
	if len(names) != len(want) {
		t.Fatalf("interfaceNames() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("interfaceNames()[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestServerConfigDefaults(t *testing.T) {
	server := NewServerBuilder(ServerConfig{}).Build()
	if got := server.Config().MaxFrameSize; got != MaxFrameSizeDefault {
		t.Fatalf("MaxFrameSize = %d, want %d", got, MaxFrameSizeDefault)
	}
	server = NewServerBuilder(ServerConfig{MaxFrameSize: 42}).Build()
	if got := server.Config().MaxFrameSize; got != 42 {
		t.Fatalf("MaxFrameSize = %d, want 42", got)
	}
}

func TestSplitMethod(t *testing.T) {
	tests := []struct {
		method  string
		iface   string
		name    string
		wantErr bool
	}{
		{"org.example.Ping", "org.example", "Ping", false},
		{"a.b.c.Method", "a.b.c", "Method", false},
		{"Ping", "", "", true},
		{".Ping", "", "", true},
		{"org.example.", "", "", true},
	}
	for _, tt := range tests {
		iface, name, err := splitMethod(tt.method)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("splitMethod(%q) unexpectedly succeeded", tt.method)
			}
			continue
		}
		if err != nil {
			t.Fatalf("splitMethod(%q) error = %v", tt.method, err)
		}
		if iface != tt.iface || name != tt.name {
			t.Fatalf("splitMethod(%q) = %q, %q, want %q, %q", tt.method, iface, name, tt.iface, tt.name)
		}
	}
}

func TestServerServeRequiresValidCallShape(t *testing.T) {
	// A method name that cannot be split is answered with
	// InvalidParameter rather than closing the connection.
	builder := NewServerBuilder(ServerConfig{})
	mustInterface(t, builder, "interface org.example.test\n\nmethod Ping() -> ()\n")

	conn := &scriptedConn{reader: bytes.NewBufferString(`{"method":"nosuchshape"}` + "\x00")}
	if err := builder.Build().Serve(context.Background(), conn); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := conn.writer.String(); !strings.Contains(got, ErrorInvalidParameter) {
		t.Fatalf("reply = %q, want InvalidParameter", got)
	}
}
