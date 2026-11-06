# Interoperability certification

This directory contains a standalone Go module and a set of Python scripts that exercise the Go v2 implementation against the reference Python Varlink implementation in both directions.

The harness covers:

- the echo battery: Go client and Python client against Go server and Python server;
- the `org.varlink.certification` scenario in both directions;
- raw wire-level probes for MUST-level framing rules;
- deliberately malformed server replies to compare client-side strictness.

## Requirements

- Linux.
- Go 1.26 or newer.
- Python 3 with the reference implementation installed.

## Running

From the repository root:

```sh
sh certification/run-interop.sh
```

The script builds `go-server` and `go-client` in a temporary directory, starts the servers, runs every leg, and asserts each expected check. Results are written to a temporary directory that is removed on exit.

The same matrix runs in `.github/workflows/certification.yml`.
