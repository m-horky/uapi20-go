---
title: Simple request-answer calls
---

# Simple request-answer calls

The common case: one call, one reply. `Invoke` sends the input parameters
and decodes the output parameters into `out`:

```go
type coordinate struct {
    Longitude float64 `json:"longitude"`
    Latitude  float64 `json:"latitude"`
    Distance  int64   `json:"distance"`
}

type configuration struct {
    Speed      int64 `json:"speed"`
    Trajectory int64 `json:"trajectory"`
    Duration   int64 `json:"duration"`
}

var out configuration
err = client.Invoke(ctx, "org.example.ftl.CalculateConfiguration", map[string]any{
    "current": coordinate{Longitude: 27.13, Latitude: -12.4, Distance: 48732498234},
    "target":  coordinate{Longitude: -48.7, Latitude: 12.9, Distance: 354667658787},
}, &out)
```

`in` and `out` may be any Go value that marshals to / unmarshals from the
method's parameter object, or `nil` for methods without parameters.

If the call fails, `Invoke` returns a `*varlink.RemoteError`; see
[errors](errors.md).

## On the wire

```json
{"method": "org.example.ftl.CalculateConfiguration", "parameters": {"current": {...}, "target": {...}}}
{"parameters": {"configuration": {"speed": 32434234, "trajectory": 686787, "duration": 13256445}}}
```

Each message is UTF-8 JSON terminated by a `NUL` byte. Replies come in
request order; there is no multiplexing.
