---
title: Errors
---

# Errors

Varlink errors are interface-specific, identified by a fully-qualified
reverse-domain name, and carry optional machine-readable parameters. There
are no error numbers and no mandatory human-readable messages.

## Checking errors from calls

Calls that fail return a `*varlink.RemoteError`. Check it by name with
`varlink.IsServiceError`, or decode its parameters:

```go
err := client.Invoke(ctx, "org.example.ftl.Jump", params, &out)
if err != nil {
    var re *varlink.RemoteError
    if errors.As(err, &re) && re.Name == "org.example.ftl.NotEnoughEnergy" {
        // refill tylium and retry
    } else if varlink.IsServiceError(err, "org.example.ftl.ParameterOutOfRange") {
        var detail struct {
            Field string `json:"field"`
        }
        if decodeErr := re.Decode(&detail); decodeErr == nil {
            log.Printf("bad field: %s", detail.Field)
        }
    } else {
        log.Fatal(err)
    }
}
```

`IsServiceError` also matches the generic errors of
`org.varlink.service` (`InterfaceNotFound`, `MethodNotFound`,
`MethodNotImplemented`, `InvalidParameter`, `PermissionDenied`,
`ExpectedMore`, `ExpectedUpgrade`).

## Replying with an error

On the server side, handlers use `ReplyError` with the fully-qualified name
and optional parameters:

```go
ftl.Unary("Jump", func(ctx context.Context, call varlink.UnaryCall) error {
    var in jumpParameters
    if err := call.Decode(&in); err != nil {
        return call.ReplyError(ctx, varlink.ErrorInvalidParameter,
            map[string]any{"parameter": "configuration"})
    }
    if in.Configuration.Speed > maxSpeed {
        return call.ReplyError(ctx, "org.example.ftl.ParameterOutOfRange",
            map[string]any{"field": "configuration.speed"})
    }
    return call.Reply(ctx, nil)
})
```

An error reply always terminates the call it belongs to, including streams.
