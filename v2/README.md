# go/varlink v2

This repository contains an implementation of the Varlink protocol in Go.

It is an AI fork of the official [varlink/go](https://github.com/varlink/go) repository, exposing a `v2` API based on [noxiouz/v2](https://github.com/noxiouz/go/tree/v2) branch, further extended to target [the UAPI.20](https://uapi-group.org/specifications/specs/varlink/) Varlink specification.

Its implementation is very much AI-driven, with some human oversight. The intent isn't to replace the official library, but to provide an early view into possible APIs an UAPI-conforming version of the library would expose. Correctness of the implementation is _a side effect_, however desirable.

## Goals

- Keep v1 stable while the redesigned runtime evolves.
- Make streaming, upgrade, and error handling first-class API concepts.
- Separate runtime policy from wire framing and code generation.
- Add defensive defaults such as bounded frame sizes and transport-level deadlines.
- Support UAPI.20.

## Migration Strategy

1. Experiment with the v2 runtime API in this module.
2. Port generators to emit v2-aware typed clients and servers.
3. Deprecate the legacy closure-based APIs once the generated code uses the new runtime.

## Initial Package Direction

- `varlink`: public client/server/runtime API
- `internal/codec`: framing and wire encode/decode
- `internal/transport`: connection and deadline helpers

## Interface Metadata

The v2 generator supports method-mode annotations in interface comments:

```varlink
# @mode stream
method Watch() -> (value: string)

# @mode oneway
method Notify(value: string) -> ()

# @mode upgrade
method Bridge() -> ()
```

Without an explicit `@mode`, methods default to unary request/reply.

## Batching

The v2 client exposes a batch API for pipelined unary and oneway calls:

```go
batch := client.Batch()
batch.Invoke("org.example.Ping", in, &out)
batch.Oneway("org.example.Notify", notify)
err := batch.Send(ctx)
```
