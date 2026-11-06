#!/usr/bin/env python3
"""One-shot Python client used for bad-server probing: fetches the echo
interface and makes a single Ping call.

Usage: python3 python-single.py <address>
"""

import json
import sys
from typing import Optional

import varlink


def error_result(err: Optional[BaseException]) -> str:
    if err is None:
        return "OK"
    if isinstance(err, varlink.VarlinkError):
        try:
            params = err.parameters() or {}
        except Exception:
            params = {}
        return "REMOTE %s %s" % (
            err.error(),
            json.dumps(params, sort_keys=True, separators=(",", ":")),
        )
    return "LOCAL %s: %s" % (type(err).__name__, err)


def main() -> None:
    address = sys.argv[1]
    client = varlink.Client(address)
    with client.open("com.example.echo") as con:
        try:
            ret = con.Ping(ping="x")
            print("[single Ping] OK %s" % ret.get("pong"))
        except Exception as e:
            print("[single Ping] %s" % error_result(e))


if __name__ == "__main__":
    main()
