---
title: Streaming replies (more)
---

# Streaming replies (`more`)

Some methods reply multiple times to a single call: monitors, subscriptions,
chunked data. Setting `more` on a call requests this. The client API is
`Stream`, which returns a receiver you loop over:

```go
type driveCondition struct {
    State       string `json:"state"` // enum: idle, spooling, busy
    TyliumLevel int64  `json:"tylium_level"`
}

stream, err := client.Stream(ctx, "org.example.ftl.Monitor", nil)
if err != nil {
    log.Fatal(err)
}
defer stream.Close()

for {
    var cond driveCondition
    if err := stream.Recv(ctx, &cond); err != nil {
        if err == io.EOF { // final reply received, stream ended
            break
        }
        log.Fatal(err) // may be a *varlink.RemoteError
    }
    fmt.Println(cond.State, cond.TyliumLevel)
}
```

A client no longer interested in the replies closes the connection (here:
`stream.Close()`), and the service aborts the operation if possible.

## Serving a streaming method

On the server side, register a `StreamHandler`. Send any number of replies
with `Send`, then exactly one final reply with `Close` (or an error reply):

```go
ftl.Stream("Monitor", func(ctx context.Context, call varlink.StreamCall) error {
    for {
        cond := readDriveCondition()
        if cond.State == "busy" {
            // final reply, connection is free for the next call
            return call.Close(ctx, cond)
        }
        if err := call.Send(ctx, cond); err != nil {
            return err // client closed the connection
        }
    }
})
```

While replies with `continues` are being sent, the server does not process
further calls on the connection (clients may still enqueue them). If the
method only makes sense with `more`, the server should reply with
`org.varlink.service.ExpectedMore` when called without it.
