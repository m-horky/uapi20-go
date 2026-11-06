---
title: Connection upgrades
---

# Connection upgrades

Setting `upgrade` on a call requests that the connection be taken over by a
custom protocol, useful for transferring larger amounts of data or foreign
payloads that are not reasonably encoded in JSON. It is conceptually similar
to the WebSocket upgrade in HTTP.

## Client

```go
conn, err := client.Upgrade(ctx, "org.example.ftl.RawTelemetry", params, &out)
if err != nil {
    log.Fatal(err) // may be a *varlink.RemoteError; connection stays varlink
}
defer conn.Close()

// The connection is no longer varlink; both sides may exchange arbitrary bytes.
io.Copy(conn, telemetrySource)
```

If the service replies with an error, the connection remains a varlink
connection and the client stays usable. After a successful upgrade, speaking
varlink to the service again requires a new connection.

## Server

```go
ftl.Upgrade("RawTelemetry", func(ctx context.Context, call varlink.UpgradeCall) error {
    conn, err := call.Accept(ctx, nil) // nil: no output parameters
    if err != nil {
        return err
    }
    defer conn.Close()

    _, err = io.Copy(conn, telemetrySink)
    return err
})
```

`Accept` sends the confirming reply and hands ownership of the raw
connection to the handler. Reply with `call.ReplyError` instead to decline
the upgrade and keep the connection in varlink mode.
