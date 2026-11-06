#!/usr/bin/env python3
"""A deliberately misbehaving varlink server used to compare client-side
strictness of the Go and Python clients.

It answers org.varlink.service.GetInterfaceDescription correctly, then
answers the next method call with a canned invalid reply, depending on the
mode:

  B1 reply-with-method   reply object carries a stray "method" member
  B2 continues-on-unary  reply object sets "continues" for a unary call
  B3 unknown-member      reply object carries an unknown member "foo"
  B4 non-object          reply is a bare JSON number
  B5 error-continues     error reply that also sets "continues"

Usage: python3 bad-server.py <address> <mode>
"""

import json
import os
import socket
import sys
import threading
from typing import Any, Dict, Optional

HERE = os.path.dirname(os.path.abspath(__file__))

with open(os.path.join(HERE, "com.example.echo.varlink")) as f:
    ECHO_DESCRIPTION = f.read()

BAD_REPLIES: Dict[str, Optional[Dict[str, Any]]] = {
    "B1 reply-with-method": {"parameters": {"pong": "x"}, "method": "evil"},
    "B2 continues-on-unary": {"parameters": {"pong": "x"}, "continues": True},
    "B3 unknown-member": {"parameters": {}, "foo": 1},
    "B4 non-object": None,  # bare JSON number
    "B5 error-continues": {"error": "com.example.echo.Bad", "continues": True},
}


def frame(obj: Any) -> bytes:
    return json.dumps(obj).encode("utf-8") + b"\0"


def serve(conn: socket.socket, mode: str) -> None:
    data = b""
    replied_bad = False
    while True:
        chunk = conn.recv(65536)
        if not chunk:
            return
        data += chunk
        while b"\0" in data:
            msg, _, data = data.partition(b"\0")
            if not msg:
                continue
            try:
                req = json.loads(msg)
            except ValueError:
                conn.close()
                return
            method = req.get("method", "")
            if method == "org.varlink.service.GetInterfaceDescription":
                conn.sendall(frame({"parameters": {"description": ECHO_DESCRIPTION}}))
            elif method == "org.varlink.service.GetInfo":
                conn.sendall(
                    frame(
                        {
                            "parameters": {
                                "vendor": "Bad",
                                "product": "Bad",
                                "version": "1",
                                "url": "",
                                "interfaces": [
                                    "com.example.echo",
                                    "org.varlink.service",
                                ],
                            }
                        }
                    )
                )
            elif not replied_bad:
                replied_bad = True
                bad = BAD_REPLIES[mode]
                if bad is None:
                    conn.sendall(b"123\0")
                else:
                    conn.sendall(frame(bad))
            else:
                conn.sendall(frame({"parameters": {"pong": "late"}}))


def main() -> None:
    address, mode = sys.argv[1], sys.argv[2]
    if address.startswith("unix:"):
        path = address[5:]
        if path.startswith("@"):
            path = "\0" + path[1:]
        s = socket.socket(socket.AF_UNIX)
        s.bind(path)
    else:
        host, port = address[4:].rsplit(":", 1)
        s = socket.socket()
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind((host, int(port)))
    s.listen(5)
    print("bad-server: listening on", address, "mode", mode)
    while True:
        conn, _ = s.accept()
        threading.Thread(target=serve, args=(conn, mode), daemon=True).start()


if __name__ == "__main__":
    main()
