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

## The model

The regular call and reply methods of both the client and the server work
for all traffic and have no file-descriptor-aware results. File descriptor
passing is opt-in per call, the way systemd's `sd_varlink_push_fd` queues
descriptors for the next message:

- to **send** descriptors, queue them before the call (client) or before
  the reply (server): `varlink.SendFiles` on the client, `PushFile` and
  `PushFiles` on a server call;
- to **receive** descriptors, opt in per call with `varlink.ReceiveFiles`,
  which stores the `varlink.Files` received with the reply.

```go
type Files []*os.File
func (f Files) Close()
```

The JSON parameters of the message reference the descriptors by their
index in the list; which parameter fields carry indices is defined by the
interface. `PushFile` returns the index it assigned, so a handler can
build its reply parameters with it.

Ownership follows the same rules as the rest of the API: descriptors
stored by `ReceiveFiles` are yours to close (`Close` closes all of them),
while descriptors you send are duplicated by the kernel when the message
carrying them is written, so your copies may be closed once the call or
the reply that carries them has been written; in a batch, the messages
are written only by `Batch.Send`, so keep the files open until it
returns. On the server that means a
descriptor passed to `PushFile` must stay open until the reply is written;
the library never closes it, not even when the reply is never sent.
Descriptors received with an error reply, or by a call that passed no
`ReceiveFiles`, are closed by the library, so they cannot leak.

Both sides can distinguish "this transport cannot carry file descriptors"
from "no descriptors were present": `Client.SupportsFileDescriptors()` and
`UnaryCall.SupportsFileDescriptors()` (likewise on `StreamCall` and
`UpgradeCall`) report the capability, and calls fail with
`varlink.ErrFileDescriptorsUnsupported` when files are passed on an
incapable transport.

## Client

Enable receiving in `ClientConfig` and pass and receive files through
call options on the regular call methods.

```go
client := varlink.NewClient(conn, varlink.ClientConfig{
    AcceptFileDescriptors: true, // only if you call methods that return files
})

// Send files with a call and receive them with the reply:
var files varlink.Files
err := client.Invoke(ctx, "org.example.editor.OpenDocument", params, &out,
    varlink.SendFiles(varlink.Files{inputFile}),
    varlink.ReceiveFiles(&files))
defer files.Close()

// Oneway, streaming (the opening call), and upgrade calls send files the
// same way; streaming replies and the upgrade reply receive them too:
err = client.Oneway(ctx, "org.example.editor.Autosave", params,
    varlink.SendFiles(varlink.Files{tempFile}))
err = stream.Recv(ctx, &out, varlink.ReceiveFiles(&files))
conn, err := client.Upgrade(ctx, "org.example.editor.TakeOver",
    params, &out, varlink.SendFiles(varlink.Files{socket}))
```

`ReceiveFiles` stores the descriptors only when the call succeeds;
descriptors attached to an error reply are closed by the library.
`ReceiveFiles` is not applicable to `Oneway` and to the opening `Stream`
call (which receive no reply), and `SendFiles` is not applicable to
`Stream.Recv`; passing them there fails with
`varlink.ErrInvalidCallOption`.

Batched calls carry files per message too, through the same options on
`Batch.Invoke` and `Batch.Oneway`; a `ReceiveFiles` option on a batched
`Invoke` stores the reply's descriptors once `Batch.Send` has succeeded.

`client.SupportsFileDescriptors()` reports whether the connection can pass
file descriptors at all (it cannot over TCP or the HTTP bridge).

## Server

Enable receiving in `ServerConfig` and take the descriptors from the call.
The first `Files()` call transfers ownership and later calls return nil,
so a handler that calls it twice cannot close the same descriptors twice:
descriptors the handler does not take are closed when it returns.

```go
srv := varlink.NewServerBuilder(varlink.ServerConfig{
    AcceptFileDescriptors: true, // only for interfaces that use them
})

iface.Unary("OpenDocument", func(ctx context.Context, call varlink.UnaryCall) error {
    files := call.Files() // ownership transfers on first call
    if len(files) != 1 {
        return call.ReplyError(ctx, varlink.ErrorInvalidParameter,
            map[string]any{"parameter": "fd"})
    }
    defer files[0].Close()
    // ... or queue descriptors of your own for the reply; the reply's
    // parameters reference them by the returned index:
    index, err := call.PushFile(resultFile)
    if err != nil {
        return err
    }
    return call.Reply(ctx, OpenDocumentReply{Result: index})
})
```

Every reply path sends the descriptors queued for it: `UnaryCall.Reply`,
`ReplyError`, `StreamCall.Send` and `Close`, and `UpgradeCall.Accept`.
The queue is emptied by each reply, and a reply suppressed because the
call is oneway does not send the queued descriptors; the caller's copies
stay valid in every case.

On Linux 6.16 and later, unix listeners with `AcceptFileDescriptors` off
disable the `SO_PASSRIGHTS` socket option, so a client sending descriptors
has its call fail rather than the descriptors being queued.

Since neither HTTP nor WebSocket connections can carry file descriptors,
methods relying on them are not available through the
[HTTP bridge](http-bridge.md).
