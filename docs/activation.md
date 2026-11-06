---
title: Socket activation and the service registry
---

# Socket activation

A listening socket may be passed to the service at startup following the
systemd socket activation protocol (`sd_listen_fds(3)`). `ActivatedListeners`
returns the passed varlink listeners, using `LISTEN_FDNAMES` to find the
descriptor named `varlink` when more than one is passed, and verifying
`LISTEN_PID` (and `LISTEN_PIDFDID` where available):

```go
listeners, err := varlink.ActivatedListeners()
if err != nil {
    log.Fatal(err)
}
if len(listeners) == 0 {
    // not socket-activated; fall back to creating a listener
}
for _, ln := range listeners {
    go ln.Serve(ctx)
}
```

Because varlink is strictly point-to-point with no buffering component,
services can implement exit-on-idle in a race-free manner: when there is no
background task and no clients connected, close the listening socket and
exit; the service activator queues incoming connections and restarts the
service as needed.

# Service registry

To make a service discoverable without enumerating all socket inodes, bind
(or symlink) the entrypoint socket into `/run/varlink/registry/`, named
after the interfaces it implements:

```go
_, err := varlink.RegisterInRegistry(
    []string{"org.example.ftl"}, // register under each interface
    "/run/org.example.ftl",      // the entrypoint socket path
)
defer varlink.UnregisterFromRegistry([]string{"org.example.ftl"})
```

On kernels that support extended attributes on socket inodes, listeners
created by the server are tagged with the `user.varlink` extended attribute
(roles `listen`, `entrypoint`, ...) so that Varlink sockets can be
recognized and enumerated. This is best-effort and never an error.
