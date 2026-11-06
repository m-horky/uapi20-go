# go/varlink

This repository contains an implementation of the Varlink protocol in Go.

It is an AI fork of the official [varlink/go](https://github.com/varlink/go) repository, exposing a `v2` API based on [noxiouz/v2](https://github.com/noxiouz/go/tree/v2) branch, further extended to target [the UAPI.20](https://uapi-group.org/specifications/specs/varlink/) Varlink specification.

Its implementation is very much AI-driven, with some human oversight. The intent isn't to replace the official library, but to provide an early view into possible APIs an UAPI-conforming version of the library would expose. Correctness of the implementation is _a side effect_, however desirable.

## License

[Apache 2.0](./LICENSE)
