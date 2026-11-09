---
title: The HTTP/WebSocket bridge
---

# The HTTP/WebSocket bridge

The `httpbridge` package encapsulates varlink services behind an HTTP
endpoint, making them available to environments where the `AF_UNIX`
transport is not available, such as web browsers. It serves two modes:
a *simple HTTP mode* mapping one HTTP request to one method call, and a
*WebSocket mode* tunneling the varlink wire protocol verbatim.

```go
bridge, err := httpbridge.New(httpbridge.Config{
    Services: map[string]string{
        // service name -> varlink address
        "org.example.ftl": "unix:/run/org.example.ftl",
    },
    // TLS, if set, serves https:// and wss://. For mutual TLS, set
    // ClientCAs and ClientAuth = tls.RequireAndVerifyClientCert.
    // TLS: &tls.Config{...},
    // Authorize, if set, guards every request (401 on error).
    // Authorize: func(r *http.Request) error { return nil },
})
if err != nil {
    log.Fatal(err)
}
log.Fatal(bridge.ListenAndServe(ctx, ":1031"))
```

`bridge.Handler()` returns an `http.Handler` for embedding instead.

## Simple mode

One HTTP request per method call; the request body is the (unwrapped)
`parameters` object:

```console
$ curl -s -X POST https://host:1031/call/org.example.ftl.Jump --json '{"speed": 3}'
```

Varlink errors are mapped to HTTP status codes
(`InvalidParameter` -> `400`, `InterfaceNotFound` -> `404`,
`MethodNotImplemented` -> `501`, `PermissionDenied` -> `403`, others ->
`500`), with the error name in the JSON body. Introspection endpoints
`GET /services`, `GET /services/{service}` and
`GET /services/{service}/{interface}` are served as specified.

### Streaming replies

Methods that reply multiple times are called with `?more=true`; the bridge
streams the replies as a JSON text sequence (RFC 7464,
`application/json-seq`):

```console
$ curl -s -H "Accept: application/json-seq" \
    https://host:1031/call/org.example.ftl.Monitor?more=true
```

Simple mode cannot issue `oneway` calls, upgrades, or pipelining; use
WebSocket mode for those.

## WebSocket mode

`GET /ws/services/{service}` upgrades to a WebSocket and acts as a
transparent byte bridge to the service; the client speaks the raw varlink
wire protocol (JSON messages terminated by `NUL`), with the full feature
set including `oneway`, `more`, `upgrade` and pipelining:

```console
$ printf '{"method":"org.example.ftl.Monitor","more":true}\0' | \
    websocat --binary wss://host:1031/ws/services/org.example.ftl | tr -d '\0'
```

A deployment exposed to a network should use TLS (`https://`/`wss://`) and
authenticate clients, e.g. via mutual TLS. Note that the services behind
the bridge see the bridge's peer credentials, not the remote client's.

## File descriptors

Neither HTTP nor WebSocket connections can carry `SCM_RIGHTS` ancillary
messages, so the bridge never accepts or forwards file descriptors, in
either mode: it refuses to start when configured with
`ClientConfig.AcceptFileDescriptors`, and any descriptors a service would
attach to a reply are dropped rather than delivered. Parameters that
reference descriptor indices are still forwarded verbatim, since detecting
such references requires interface-specific knowledge; interfaces that
pass file descriptors should document how their methods fail when used
through the bridge.
