#!/usr/bin/env python3
"""Raw wire-level probes against a varlink server.

Sends hand-crafted frames directly over the socket and records whether the
server replies, stays silent, or closes the connection. Used to compare the
Go and Python server implementations at the protocol level.

Usage: python3 probe.py <address>
"""

import json
import socket
import sys
import time
from typing import Any, Callable, List, Optional, Sequence, Tuple

TIMEOUT = 2.0


def connect(address: str) -> socket.socket:
    if address.startswith("unix:"):
        path = address[5:]
        if path.startswith("@"):
            path = "\0" + path[1:]
        s = socket.socket(socket.AF_UNIX)
        s.connect(path)
        return s
    if address.startswith("tcp:"):
        host, port = address[4:].rsplit(":", 1)
        return socket.create_connection((host, int(port)))
    raise SystemExit("bad address %r" % address)


def recv_reply(s: socket.socket, expect_n: int = 1) -> Tuple[List[bytes], bool]:
    """Read up to expect_n null-terminated replies. Returns (replies, closed)."""
    s.settimeout(TIMEOUT)
    data = b""
    replies = []
    closed = False
    deadline = time.time() + TIMEOUT
    while len(replies) < expect_n and time.time() < deadline:
        try:
            chunk = s.recv(65536)
        except socket.timeout:
            break
        except ConnectionResetError:
            closed = True
            break
        if not chunk:
            closed = True
            break
        data += chunk
        while b"\0" in data:
            frame, _, data = data.partition(b"\0")
            replies.append(frame)
    return replies, closed


def recv_raw(s: socket.socket) -> Tuple[bytes, bool]:
    """Read whatever raw bytes arrive (no frame delimiter)."""
    s.settimeout(TIMEOUT)
    out = b""
    closed = False
    deadline = time.time() + TIMEOUT
    while time.time() < deadline:
        try:
            chunk = s.recv(65536)
        except socket.timeout:
            break
        except ConnectionResetError:
            closed = True
            break
        if not chunk:
            closed = True
            break
        out += chunk
        break
    return out, closed


def probe(
    name: str,
    address: str,
    frames: Sequence[bytes],
    expect_n: int = 1,
    then: Optional[Callable[[socket.socket], Optional[List[str]]]] = None,
) -> None:
    """Run one probe: send frames, read replies, optionally run a follow-up."""
    s = connect(address)
    outcome = []
    try:
        for f in frames:
            s.sendall(f)
        replies, closed = recv_reply(s, expect_n)
        if replies:
            for r in replies:
                outcome.append("REPLY %s" % r.decode("utf-8", "replace"))
        if closed:
            outcome.append("CLOSED")
        if not replies and not closed:
            outcome.append("TIMEOUT")
        if then and not closed:
            extra = then(s)
            if extra:
                outcome.extend(extra)
    except (BrokenPipeError, ConnectionResetError, OSError) as e:
        outcome.append("SEND-ERROR %s" % e)
    finally:
        s.close()
    print("[%s] %s" % (name, " | ".join(outcome)))


def main() -> None:
    address = sys.argv[1]

    def F(obj: Any) -> bytes:
        return json.dumps(obj).encode("utf-8") + b"\0"

    ping = {"method": "com.example.echo.Ping", "parameters": {"ping": "x"}}

    # P1: invalid JSON
    probe("P1 invalid-json", address, [b"{\0"])

    # P2: valid JSON, not an object
    probe("P2 non-object", address, [b"123\0"])

    # P3: unknown top-level member
    probe("P3 unknown-member", address, [F(dict(ping, foo=1))])

    # P4: oneway and more set at the same time
    probe(
        "P4 two-flags",
        address,
        [
            F(
                {
                    "method": "com.example.echo.Notify",
                    "parameters": {"message": "x"},
                    "oneway": True,
                    "more": True,
                }
            )
        ],
    )

    # P5: parameters not an object
    probe(
        "P5 params-array",
        address,
        [F({"method": "com.example.echo.Ping", "parameters": [1]})],
    )

    # P6: upgrade flag on a plain unary method
    def p6_followup(s: socket.socket) -> List[str]:
        try:
            s.sendall(b"hello-upgraded\n")
            data, closed = recv_raw(s)
            out = []
            if data:
                out.append("AFTER-UPGRADE-DATA %r" % data)
            if closed:
                out.append("CLOSED-AFTER")
            if not data and not closed:
                out.append("SILENT-AFTER")
            return out
        except OSError as e:
            return ["AFTER-UPGRADE-ERROR %s" % e]

    probe(
        "P6 upgrade-on-unary",
        address,
        [
            F(
                {
                    "method": "com.example.echo.Ping",
                    "parameters": {"ping": "x"},
                    "upgrade": True,
                }
            )
        ],
        then=p6_followup,
    )

    # P7: client sends a reply-shaped message (error member, no method)
    probe("P7 client-error-msg", address, [F({"error": "some.Error"})])

    # P8: continues flag in a call
    probe("P8 continues-in-call", address, [F(dict(ping, continues=True))])

    # P9: vendor extension object member
    probe(
        "P9 vendor-extension", address, [F(dict(ping, **{"org.example": {"foo": 1}}))]
    )

    # P10: empty frame
    probe("P10 empty-frame", address, [b"\0"])

    # P11: frame larger than 1 MiB (Go default max frame size)
    big = {
        "method": "com.example.echo.Ping",
        "parameters": {"ping": "x" * (2 * 1024 * 1024)},
    }
    probe("P11 large-frame", address, [F(big)])

    # P12: three pipelined calls in a single write
    probe("P12 pipelined", address, [F(ping), F(ping), F(ping)], expect_n=3)

    # P13: explicit null parameters
    probe(
        "P13 null-params",
        address,
        [F({"method": "com.example.echo.Ping", "parameters": None})],
    )

    # P14: oneway call to a streaming method without more
    probe(
        "P14 oneway-no-more",
        address,
        [
            F(
                {
                    "method": "com.example.echo.Watch",
                    "parameters": {"count": 2},
                    "oneway": True,
                }
            )
        ],
    )

    # P15: non-boolean oneway flag
    probe(
        "P15 flag-not-bool",
        address,
        [
            F(
                {
                    "method": "com.example.echo.Notify",
                    "parameters": {"message": "x"},
                    "oneway": "yes",
                }
            )
        ],
    )

    # P16: upgrade method called without the upgrade flag
    probe("P16 bridge-no-upgrade", address, [F({"method": "com.example.echo.Bridge"})])

    # P17: upgrade method called with the upgrade flag, then raw protocol
    def p17_followup(s: socket.socket) -> List[str]:
        try:
            s.sendall(b"raw-protocol-line\n")
            data, closed = recv_raw(s)
            out = []
            if data:
                out.append("RAW-REPLY %r" % data)
            if closed:
                out.append("CLOSED-AFTER")
            if not data and not closed:
                out.append("SILENT-AFTER")
            return out
        except OSError as e:
            return ["RAW-ERROR %s" % e]

    probe(
        "P17 bridge-upgrade",
        address,
        [F({"method": "com.example.echo.Bridge", "upgrade": True})],
        then=p17_followup,
    )

    # P18: method member not a string
    probe("P18 method-not-string", address, [F({"method": 123})])


if __name__ == "__main__":
    main()
