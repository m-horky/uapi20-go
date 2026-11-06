---
title: Pipelined batches
---

# Pipelining calls

Multiple requests can be queued on a connection before the replies arrive
(pipelining). The `Batch` API collects unary and oneway calls and sends them
with one `Send`:

```go
batch := client.Batch()
for i := 0; i < 100; i++ {
    batch.Invoke("org.example.ftl.CalculateConfiguration", input[i], &output[i])
    batch.Oneway("org.example.log.Notify", notifications[i])
}
if err := batch.Send(ctx); err != nil {
    log.Fatal(err)
}
```

`Send` writes all queued calls, then reads the replies in order and decodes
them into the registered `out` values. The first error aborts the batch.

Notes:

- Only unary (`Invoke`) and `oneway` calls can be batched; streams and
  upgrades need exclusive use of the connection.
- File descriptors cannot be passed in batched calls; replies carrying them
  have their descriptors discarded.
- Oneway calls in a batch produce no reply, so the replies are matched to
  the non-oneway calls in order.
