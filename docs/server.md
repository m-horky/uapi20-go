---
title: Writing a service
---

# Writing a service

A service is built with `ServerBuilder`: set the metadata exposed via
`org.varlink.service`, register each interface with its handlers, build,
listen, serve.

```go
const description = `interface org.example.ftl

type DriveCondition (
  state: (idle, spooling, busy),
  tylium_level: int
)

method Monitor() -> (condition: DriveCondition)

method Jump(speed: int) -> ()
`

srv := varlink.NewServerBuilder(varlink.ServerConfig{
    MaxFrameSize: varlink.MaxFrameSizeDefault,
}).SetInfo(varlink.ServiceInfo{
    Vendor:  "Example Corp",
    Product: "FTL Drive",
    Version: "0.1",
    URL:     "https://example.com/ftl",
})

ftl, err := srv.Interface(description) // parses and validates the IDL
if err != nil {
    log.Fatal(err)
}

ftl.Unary("Jump", func(ctx context.Context, call varlink.UnaryCall) error {
    var in struct {
        Speed int64 `json:"speed"`
    }
    if err := call.Decode(&in); err != nil {
        return call.ReplyError(ctx, varlink.ErrorInvalidParameter,
            map[string]any{"parameter": "speed"})
    }
    return call.Reply(ctx, nil)
})

server := srv.Build()

listener, err := server.Listen(ctx, "unix:/run/org.example.ftl")
if err != nil {
    log.Fatal(err)
}
log.Fatal(listener.Serve(ctx))
```

`Interface` parses the interface description (with documentation comments)
and validates the method registrations against it; handlers can only be
registered for methods the interface declares. The built-in
`org.varlink.service` interface (`GetInfo`,
`GetInterfaceDescription`) is served automatically.

## Handler kinds

| Registration | Use case                                            | Call API                                        |
|--------------|-----------------------------------------------------|-------------------------------------------------|
| `Unary`      | one request, one reply (also serves `oneway` calls) | `UnaryCall`: `Reply`, `ReplyError`, `IsOneway`, `Files`, `PushFile`, `PushFiles`, `SupportsFileDescriptors` |
| `Stream`     | multiple replies per call (`more`)                  | `StreamCall`: `Send`, `Close`, `ReplyError`, `Files`, `PushFile`, `PushFiles`, `SupportsFileDescriptors` |
| `Upgrade`    | connection takeover (`upgrade`)                     | `UpgradeCall`: `Accept`, `ReplyError`, `Files`, `PushFile`, `PushFiles`, `SupportsFileDescriptors` |

See the [streams](streams.md) and [upgrades](upgrade.md) pages.

## Authorization

`ServerConfig.Authorize` is called before every method call is dispatched.
The context carries the peer credentials of the connection (see
`varlink.PeerFromContext` on unix transports). Returning a `*RemoteError`
replies with that error; any other error replies with
`org.varlink.service.PermissionDenied`:

```go
srv := varlink.NewServerBuilder(varlink.ServerConfig{
    Authorize: func(ctx context.Context, method string) error {
        peer, ok := varlink.PeerFromContext(ctx)
        if ok && peer.UID != 0 {
            return fmt.Errorf("root only")
        }
        return nil
    },
})
```

## Runtime policy

`ServerConfig` also carries defensive defaults worth setting:
`MaxFrameSize` (connections exceeding it are closed), `ReadTimeout` /
`WriteTimeout` per message, `MaxConnections` per listener, and `Logger` /
`ErrorHandler` for connection-level errors.
