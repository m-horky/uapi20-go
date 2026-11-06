---
title: Clients: connecting and addresses
---

# Connecting to a service

Varlink services are addressed with URI-like strings:

| Type                           | Example                 |
|--------------------------------|-------------------------|
| UNIX socket                    | `unix:/run/org.example` |
| UNIX abstract namespace socket | `unix:@org.example`     |
| TCP (hostname)                 | `tcp:localhost:12345`   |
| TCP (IPv4 / IPv6)              | `tcp:127.0.0.1:12345`, `tcp:[::1]:12345` |

Properties after a `;` are parsed but ignored, per the specification.

`varlink.Dial` connects and returns a client:

```go
client, err := varlink.Dial(ctx, "unix:/run/org.example.ftl")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

## Client configuration

`varlink.NewClient` takes an `io.ReadWriteCloser` plus a
`varlink.ClientConfig` for transport policy:

```go
client := varlink.NewClient(conn, varlink.ClientConfig{
    MaxFrameSize:  varlink.MaxFrameSizeDefault,
    ReadTimeout:   30 * time.Second,
    WriteTimeout:  30 * time.Second,
    // Opt in only if you call methods that return file descriptors:
    AcceptFileDescriptors: false,
})
```

## Introspection

Every service should implement the built-in `org.varlink.service`
interface. The client exposes its two methods directly:

```go
vendor, version, interfaces, err := client.GetInfo(ctx)
if err != nil {
    log.Fatal(err)
}
fmt.Println(vendor, version, interfaces)

description, err := client.GetInterfaceDescription(ctx, "org.example.ftl")
if err != nil {
    log.Fatal(err)
}
fmt.Println(description) // the IDL, with documentation comments
```

The full method-call surface is documented in the other pages; start with
[simple request-answer calls](simple-call.md).
