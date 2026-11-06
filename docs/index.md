# uapi20-go / v2 API

This module implements the
[UAPI.20 Varlink IPC](https://uapi-group.org/specifications/specs/varlink/)
specification in Go. It is a staging area for a redesigned `v2` API of
[go-varlink](https://github.com/varlink/go), making streaming, upgrade, and
error handling first-class concepts.

```sh
go get github.com/m-horky/uapi20-go/v2
```

Import it as:

```go
import "github.com/m-horky/uapi20-go/v2/varlink"
```

API reference: [pkg.go.dev/github.com/m-horky/uapi20-go/v2/varlink](https://pkg.go.dev/github.com/m-horky/uapi20-go/v2/varlink)

## Use cases

- [Clients: connecting and addresses](client.md)
- [Simple request-answer calls](simple-call.md)
- [Streaming replies (`more`)](streams.md)
- [Fire-and-forget calls (`oneway`)](oneway.md)
- [Pipelined batches](batching.md)
- [Connection upgrades (`upgrade`)](upgrade.md)
- [Errors](errors.md)
- [Passing file descriptors](file-descriptors.md)
- [Writing a service](server.md)
- [Socket activation and the service registry](activation.md)
- [The HTTP/WebSocket bridge](http-bridge.md)

The wire protocol and interface definition language are specified by the
[UAPI.20 Varlink specification](uapi-spec.md) (a copy of the spec lives in
this repository).
