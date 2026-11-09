---
title: Fire-and-forget calls (oneway)
---

# Oneway calls

Setting `oneway` on a call instructs the service to send no reply at all,
neither success nor error. This is useful for transactions where many
messages are sent and only one final confirmation is needed, or for
non-critical data. Because of the strict ordering of the protocol, all
services must implement it.

```go
err := client.Oneway(ctx, "org.example.log.Notify", map[string]any{
    "message": "spooling complete",
})
```

There is no feedback: the client will not learn about failures, including
invocation of a non-existent method. The next reply received on the
connection belongs to the next call issued without `oneway`.

Oneway calls can be combined with [batches](batching.md) and with
[file descriptor passing](file-descriptors.md) via the `SendFiles` option
(`ReceiveFiles` is not applicable, since there is no reply):

```go
err := client.Oneway(ctx, "org.example.log.Notify", params,
    varlink.SendFiles(varlink.Files{file}))
```
