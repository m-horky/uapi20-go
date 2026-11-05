# Testing

This document describes the test suites of the `v2` module, and which tests
are expected to run in which environment. All commands below are run from
the `v2/` directory.

## Everyday development

Run after any change, before pushing:

```sh
go vet ./...
go test ./...
```

`go test ./...` covers the packages `internal/codec`, `varlink`,
`varlink/idl`, and `varlink/httpbridge`. Most tests are plain unit tests
and run in well under a second.

Before merging changes to the connection runtime (`fd_unix.go`,
`server_runtime.go`, `client_runtime.go`, `runtime.go`), also run the race
detector; those files coordinate reader, dispatcher, and handler
goroutines:

```sh
go test -race ./...
```

## Platform-dependent tests

The runtime has platform-specific parts behind build tags, and the test
suite follows them. On a given platform, the tests for unsupported
facilities are either compiled out (via matching build tags) or skip at
runtime; a passing run with skips is a successful run.

| Build tag  | Files                                          | Tests                                                      |
|------------|------------------------------------------------|------------------------------------------------------------|
| `linux`    | `activation_linux.go`, `peer_linux.go`, `passrights_linux.go`, `xattr_linux.go` | socket activation, peer credentials (`SO_PEERCRED`/`SO_PEERPIDFD`), `SO_PASSRIGHTS`, xattr tagging, registry |
| `!linux`   | the `*_other.go` shims                          | only that the shims compile and the API degrades gracefully |
| `unix`     | `fd_unix.go`                                    | SCM_RIGHTS file descriptor passing                         |
| `!unix`    | `fd_other.go`                                   | descriptor passing fails with `ErrFileDescriptorsUnsupported` |

Runtime skips (not failures) occur where the platform supports a facility
in principle but the environment does not:

- `peer_test.go`: no `SO_PEERPIDFD` (Linux < 6.5).
- `activation_test.go`: no `pidfd_open(2)`.
- `xattr_test.go`: file system without `user.*` extended attributes
  (e.g. tmpfs without xattr support).
- `fd_test.go`: the environment does not permit counting open file
  descriptors via `/proc/self/fd`.

## JSON backends

`internal/codec` has two interchangeable JSON backends, selected at build
time:

- `json_backend_std.go` (default): `encoding/json`.
- `json_backend_v2.go` (`goexperiment.jsonv2`): `encoding/json/v2` with
  v1 compatibility options.

The backend is exercised by the same `internal/codec` and `varlink` test
suites; to run them against the json/v2 backend:

```sh
GOEXPERIMENT=jsonv2 go test ./...
```

CI runs both variants.

## Certification

The certification tool validates a varlink implementation against the
official org.varlink.certification interface. It lives in the repository
root module (`cmd/varlink-go-certification`), not in `v2`, and is not part
of `go test ./...` here. When touching wire-format or service interface
behavior, run it from the repository root:

```sh
go generate ./cmd/...
go test ./cmd/varlink-go-certification/...
```

## CI (`.github/workflows/go.yml`)

On every push and pull request, the workflow builds and tests the module
on a matrix of `macos-latest`, `windows-latest`, and `ubuntu-latest`, each
with the oldest supported Go release and the current stable release. The
Linux leg is the one that exercises the `linux`- and `unix`-gated tests
(activation, peer credentials, fd passing, xattrs); the macOS leg covers
the `unix`-gated tests without the Linux-only facilities; the Windows leg
covers the `!unix` degradation paths.

Expectations per leg:

- All legs: `go build ./...` and `go test ./...` pass (skips allowed as
  listed above).
- The json/v2 backend variant runs on the Linux leg.
