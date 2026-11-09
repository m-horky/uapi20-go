/*
Package varlink an implementation of the Varlink protocol in Go.

This package is roughly based on the official github.com/varlink/go library,
exposing a v2 API based on a work done by noxiouz, further extended to target
the UAPI.20 Varlink specification.

The implementation has been fully agentic, with some human oversight.

The intent is not to replace the official library, nor to produce a production
level code that could be used long-term. The aim is to provide an early view
into possible APIs a library would expose to conform to the UAPI.20
specification while being more ergonomic to use.

Correctness of the implementation is a desirable side effect.

# Example

A service is described by an interface definition:

	interface org.example.greeter

	method Greet(name: string) -> (greeting: string)

A server object is created with varlink.NewServerBuilder

	builder := varlink.NewServerBuilder(varlink.ServerConfig{})
	builder.SetInfo(varlink.ServiceInfo{
		Vendor:  "Example",
		Product: "Greeter",
		Version: "1",
		URL:     "https://example.org",
	})

	iface, err := builder.Interface(description)
	if err != nil {
		log.Fatal(err)
	}
	err = iface.Unary("Greet", func(ctx context.Context, call varlink.UnaryCall) error {
		var in struct {
			Name string `json:"name"`
		}
		if err := call.Decode(&in); err != nil {
			return err
		}
		return call.Reply(ctx, struct {
			Greeting string `json:"greeting"`
		}{Greeting: "Hello, " + in.Name})
	})
	if err != nil {
		log.Fatal(err)
	}

	listener, err := builder.Build().Listen(ctx, "unix:/run/org.example.greeter")
	if err != nil {
		log.Fatal(err)
	}
	if err := listener.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}

A client dials the service address and invokes a method by its fully
qualified name:

	client, err := varlink.Dial(ctx, "unix:/run/org.example.greeter")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	var out struct {
		Greeting string `json:"greeting"`
	}
	err = client.Invoke(ctx, "org.example.greeter.Greet", struct {
		Name string `json:"name"`
	}{Name: "World"}, &out)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Greeting)

On AF_UNIX stream sockets, file descriptors can be passed in SCM_RIGHTS
ancillary messages attached to the messages they belong to; see the
SendFiles and ReceiveFiles call options, the PushFile and PushFiles call
methods, and the AcceptFileDescriptors configuration options. Other
transports degrade gracefully with
ErrFileDescriptorsUnsupported. The subpackage varlink/httpbridge encapsulates
services behind the HTTP and WebSocket transports of the UAPI specification.
*/
package varlink
