---
title: Passing file descriptors
---

# Passing file descriptors

`AF_UNIX` sockets allow passing file descriptors between processes via
`SCM_RIGHTS` ancillary messages, for cases where a purely in-band (JSON)
implementation is impossible or unreasonably inefficient: transferring large
amounts of data via pipes or memory-file descriptors, or handing over kernel
objects that cannot be expressed in JSON.

Receiving file descriptors is opt-in on both sides, per the specification:
clients must not accept them unless they invoke methods that return them,
and services must not accept them unless they implement an interface that
makes use of them.

## Client

Enable receiving in `ClientConfig` and use the `*FDs` methods. Descriptors
are referenced from the message parameters by their index; the interface
definition documents which parameter fields carry indices.

```go
client := varlink.NewClient(conn, varlink.ClientConfig{
    AcceptFileDescriptors: true, // only if you call methods that return files
})

// Send files with a call:
files, err := client.InvokeFDs(ctx, "org.example.editor.OpenDocument",
    params, &out, []*os.File{f})

// Receive files with a reply, including streaming replies:
files, err = stream.RecvFDs(ctx, &out)
```

`client.SupportsFileDescriptors()` reports whether the connection can pass
file descriptors at all (it cannot over TCP or the HTTP bridge).

## Server

Enable receiving in `ServerConfig` and take the descriptors from the call.
The first `FileDescriptors()` call transfers ownership: descriptors the
handler does not take are closed when it returns.

```go
srv := varlink.NewServerBuilder(varlink.ServerConfig{
    AcceptFileDescriptors: true, // only for interfaces that use them
})

iface.Unary("OpenDocument", func(ctx context.Context, call varlink.UnaryCall) error {
    files := call.FileDescriptors() // ownership transfers on first call
    if len(files) != 1 {
        return call.ReplyError(ctx, varlink.ErrorInvalidParameter,
            map[string]any{"parameter": "fd"})
    }
    defer files[0].Close()
    // ... or reply with descriptors of your own:
    return call.ReplyFDs(ctx, out, []*os.File{resultFile})
})
```

On Linux 6.16 and later, unix listeners with `AcceptFileDescriptors` off
disable the `SO_PASSRIGHTS` socket option, so a client sending descriptors
has its call fail rather than the descriptors being queued.

Since neither HTTP nor WebSocket connections can carry file descriptors,
methods relying on them are not available through the
[HTTP bridge](http-bridge.md).
