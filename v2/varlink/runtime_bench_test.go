package varlink

import (
	"context"
	"io"
	"net"
	"testing"
)

func BenchmarkConnClientInvoke(b *testing.B) {
	client, done := benchmarkClientServer(b, func(builder *ServerBuilder) {
		iface := benchInterface(b, builder, "Ping", "Watch")
		_ = iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			var in struct {
				Value string `json:"value"`
			}
			if err := call.Decode(&in); err != nil {
				return err
			}
			return call.Reply(ctx, struct {
				Value string `json:"value"`
			}{Value: in.Value})
		})
	})
	defer benchmarkClose(b, client, done)

	in := struct {
		Value string `json:"value"`
	}{Value: "payload"}

	var out struct {
		Value string `json:"value"`
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := client.Invoke(context.Background(), "org.example.bench.Ping", in, &out); err != nil {
			b.Fatalf("Invoke() error = %v", err)
		}
	}
}

func BenchmarkConnClientStream(b *testing.B) {
	client, done := benchmarkClientServer(b, func(builder *ServerBuilder) {
		iface := benchInterface(b, builder, "Ping", "Watch")
		_ = iface.Stream("Watch", func(ctx context.Context, call StreamCall) error {
			if err := call.Send(ctx, struct {
				Value string `json:"value"`
			}{Value: "one"}); err != nil {
				return err
			}
			if err := call.Send(ctx, struct {
				Value string `json:"value"`
			}{Value: "two"}); err != nil {
				return err
			}
			return call.Close(ctx, struct {
				Value string `json:"value"`
			}{Value: "three"})
		})
	})
	defer benchmarkClose(b, client, done)

	in := struct {
		Value string `json:"value"`
	}{Value: "payload"}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stream, err := client.Stream(context.Background(), "org.example.bench.Watch", in)
		if err != nil {
			b.Fatalf("Stream() error = %v", err)
		}
		for i := 0; i < 3; i++ {
			var out struct {
				Value string `json:"value"`
			}
			if err := stream.Recv(context.Background(), &out); err != nil {
				b.Fatalf("Recv() error = %v", err)
			}
		}
		if err := stream.Recv(context.Background(), nil); err != io.EOF {
			b.Fatalf("Recv() final error = %v", err)
		}
	}
}

func BenchmarkConnClientBatchInvoke(b *testing.B) {
	client, done := benchmarkClientServer(b, func(builder *ServerBuilder) {
		iface := benchInterface(b, builder, "Ping", "Watch")
		_ = iface.Unary("Ping", func(ctx context.Context, call UnaryCall) error {
			var in struct {
				Value string `json:"value"`
			}
			if err := call.Decode(&in); err != nil {
				return err
			}
			return call.Reply(ctx, struct {
				Value string `json:"value"`
			}{Value: in.Value})
		})
	})
	defer benchmarkClose(b, client, done)

	in := struct {
		Value string `json:"value"`
	}{Value: "payload"}

	var out1 struct {
		Value string `json:"value"`
	}
	var out2 struct {
		Value string `json:"value"`
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		batch := client.Batch()
		batch.Invoke("org.example.bench.Ping", in, &out1)
		batch.Invoke("org.example.bench.Ping", in, &out2)
		if err := batch.Send(context.Background()); err != nil {
			b.Fatalf("Batch.Send(ctx) error = %v", err)
		}
	}
}

func benchInterface(b *testing.B, builder *ServerBuilder, methods ...string) *Interface {
	b.Helper()
	description := "interface org.example.bench\n"
	for _, method := range methods {
		description += "\nmethod " + method + "(value: string) -> (value: string)\n"
	}
	iface, err := builder.Interface(description)
	if err != nil {
		b.Fatalf("Interface() error = %v", err)
	}
	return iface
}

func benchmarkClientServer(b *testing.B, register func(*ServerBuilder)) (*ConnClient, <-chan error) {
	b.Helper()

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

func benchmarkClose(b *testing.B, client *ConnClient, done <-chan error) {
	b.Helper()
	_ = client.Close()
	if err := <-done; err != nil {
		b.Fatalf("server.Serve() error = %v", err)
	}
}
