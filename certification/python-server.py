#!/usr/bin/env python3
"""Example varlink service used for interop testing against the Go
implementation. It implements the com.example.echo interface (see
com.example.echo.varlink) and the org.varlink.certification interface.

Usage: python3 python-server.py [address]
Default address: unix:@py-interop-echo
"""

import codecs
import json
import math
import os
import sys
import time
from typing import Any, Dict, Generator, List, NoReturn, Optional, Set

import varlink

HERE = os.path.dirname(os.path.abspath(__file__))

service = varlink.Service(
    vendor="Example",
    product="Python interop service",
    version="1",
    url="https://example.com/python",
    interface_dir=HERE,
)


@service.interface("com.example.echo")
class Echo:
    """com.example.echo implementation.

    Note on Bridge: the Python varlink server cannot hand a connection over
    to a method handler (no connection upgrade support), so Bridge is left
    unimplemented and calls are answered with
    org.varlink.service.MethodNotImplemented.
    """

    def Ping(self, ping: str) -> Dict[str, str]:
        return {"pong": ping}

    def Add(self, a: int, b: int) -> Dict[str, int]:
        return {"sum": a + b}

    def Divide(self, a: float, b: float) -> Dict[str, float]:
        return {"quotient": a / b}

    def Toggle(self, flag: bool) -> Dict[str, bool]:
        return {"flag": not flag}

    def Join(self, items: List[str], separator: Optional[str]) -> Dict[str, str]:
        return {"joined": (separator or "").join(items)}

    def Lookup(self, id: int) -> Dict[str, Any]:
        if id != 7:
            raise varlink.VarlinkError(
                {
                    "error": "com.example.echo.NoSuchRecord",
                    "parameters": {"id": id},
                }
            )
        return {
            "record": {
                "id": 7,
                "name": "seven",
                "labels": {"lang": "en", "odd": "yes"},
                "tags": ["prime", "odd"],
            }
        }

    def Words(self, text: str) -> Dict[str, Set[str]]:
        return {"words": set(text.split())}

    def Choose(self, mode: str) -> Dict[str, str]:
        return {"mode": mode}

    def CountMembers(self, value: Dict[str, Any]) -> Dict[str, int]:
        return {"members": len(value)}

    def CheckFD(self) -> Dict[str, bool]:
        # The Python implementation has no file descriptor support; any
        # descriptors sent by a client are discarded by the socket layer.
        return {"received": False}

    def Fail(self, code: int) -> NoReturn:
        raise varlink.VarlinkError(
            {
                "error": "com.example.echo.TooBusy",
                "parameters": {"retry_after": code},
            }
        )

    def Notify(self, message: str, _oneway: bool = False) -> None:
        return None

    def Watch(
        self, count: int, _more: bool = False
    ) -> Generator[Dict[str, Any], None, None]:
        if not _more:
            # The Python server does not enforce the 'more' flag; it
            # returns a single reply instead.
            yield {"index": 1}
            return
        for i in range(1, count + 1):
            yield {"index": i, "_continues": i != count}


class ServiceRequestHandler(varlink.RequestHandler):
    service = service


# ------------------------------------------------------- certification ---
# Ported from the reference implementation's test suite
# (varlink/tests/test_certification.py, varlink 31).


class CertificationError(varlink.VarlinkError):
    def __init__(self, wants: Any, got: Any) -> None:
        varlink.VarlinkError.__init__(
            self,
            {
                "error": "org.varlink.certification.CertificationError",
                "parameters": {"wants": wants, "got": got},
            },
        )


@service.interface("org.varlink.certification")
class CertService(object):
    next_method: Dict[str, str] = {}

    def new_client_id(self, _server: Any) -> str:
        client_id = codecs.getencoder("hex")(os.urandom(16))[0].decode("ascii")
        if not hasattr(_server, "next_method"):
            _server.next_method = {}
        if not hasattr(_server, "lifetimes"):
            _server.lifetimes = []
        _server.next_method[client_id] = "Start"
        _server.lifetimes.append((time.time(), client_id))
        return client_id

    def check_lifetimes(self, _server: Any) -> None:
        if not hasattr(_server, "lifetimes"):
            return
        now = time.time()
        while True:
            if len(_server.lifetimes) == 0:
                return
            (t, client_id) = _server.lifetimes[0]
            if (now - t) < (60 * 60 * 12):
                return
            del _server.lifetimes[0]
            if hasattr(_server, "next_method") and client_id in _server.next_method:
                del _server.next_method[client_id]

    def assert_raw(
        self,
        client_id: str,
        _server: Any,
        _raw: bytes,
        _message: Dict[str, Any],
        wants: Any,
    ) -> None:
        if wants != _message:
            del _server.next_method[client_id]
            raise CertificationError(wants, json.loads(_raw.decode("utf-8")))

    def assert_cmp(
        self, client_id: str, _server: Any, _raw: bytes, wants: Any, _bool: bool
    ) -> None:
        if not _bool:
            del _server.next_method[client_id]
            raise CertificationError(wants, json.loads(_raw.decode("utf-8")))

    def assert_method(
        self, client_id: str, _server: Any, from_method: str, next_method: str
    ) -> None:
        if not hasattr(_server, "next_method") or client_id not in _server.next_method:
            raise CertificationError(
                {"method": "org.varlink.certification.Start+++"},
                {"method": "org.varlink.certification." + from_method},
            )
        self.check_lifetimes(_server)
        if from_method != _server.next_method[client_id]:
            raise CertificationError(
                "Call to method org.varlink.certification."
                + _server.next_method[client_id],
                "Call to method org.varlink.certification." + from_method,
            )
        _server.next_method[client_id] = next_method

    def Start(
        self,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
        _oneway: bool = False,
    ) -> Dict[str, str]:
        client_id = self.new_client_id(_server)
        if "parameters" in _message and not _message["parameters"]:
            del _message["parameters"]
        self.assert_method(client_id, _server, "Start", "Test01")
        self.assert_raw(
            client_id,
            _server,
            _raw,
            _message,
            {
                "method": "org.varlink.certification.Start",
            },
        )
        return {"client_id": client_id}

    def Test01(
        self,
        client_id: str,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, bool]:
        self.assert_method(client_id, _server, "Test01", "Test02")
        self.assert_raw(
            client_id,
            _server,
            _raw,
            _message,
            {
                "method": "org.varlink.certification.Test01",
                "parameters": {"client_id": client_id},
            },
        )
        return {"bool": True}

    def Test02(
        self,
        client_id: str,
        _bool: bool,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, int]:
        self.assert_method(client_id, _server, "Test02", "Test03")
        wants = {
            "method": "org.varlink.certification.Test02",
            "parameters": {"client_id": client_id, "bool": True},
        }
        self.assert_cmp(client_id, _server, _raw, wants, _bool == True)
        self.assert_raw(client_id, _server, _raw, _message, wants)
        return {"int": 1}

    def Test03(
        self,
        client_id: str,
        _int: int,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, float]:
        self.assert_method(client_id, _server, "Test03", "Test04")
        wants = {
            "method": "org.varlink.certification.Test03",
            "parameters": {"client_id": client_id, "int": 1},
        }
        self.assert_cmp(client_id, _server, _raw, wants, _int == 1)
        self.assert_raw(client_id, _server, _raw, _message, wants)
        return {"float": 1.0}

    def Test04(
        self,
        client_id: str,
        _float: float,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, str]:
        self.assert_method(client_id, _server, "Test04", "Test05")
        wants = {
            "method": "org.varlink.certification.Test04",
            "parameters": {"client_id": client_id, "float": 1.0},
        }
        self.assert_cmp(client_id, _server, _raw, wants, _float == 1.0)
        self.assert_raw(client_id, _server, _raw, _message, wants)
        return {"string": "ping"}

    def Test05(
        self,
        client_id: str,
        _string: str,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        self.assert_method(client_id, _server, "Test05", "Test06")
        wants = {
            "method": "org.varlink.certification.Test05",
            "parameters": {"client_id": client_id, "string": "ping"},
        }
        self.assert_cmp(client_id, _server, _raw, wants, _string == "ping")
        self.assert_raw(client_id, _server, _raw, _message, wants)
        return {"bool": False, "int": 2, "float": math.pi, "string": "a lot of string"}

    def Test06(
        self,
        client_id: str,
        _bool: bool,
        _int: int,
        _float: float,
        _string: str,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Dict[str, Any]]:
        self.assert_method(client_id, _server, "Test06", "Test07")
        wants = {
            "method": "org.varlink.certification.Test06",
            "parameters": {
                "client_id": client_id,
                "bool": False,
                "int": 2,
                "float": math.pi,
                "string": "a lot of string",
            },
        }
        self.assert_raw(client_id, _server, _raw, _message, wants)
        self.assert_cmp(client_id, _server, _raw, wants, _int == 2)
        self.assert_cmp(client_id, _server, _raw, wants, _bool == False)
        self.assert_cmp(client_id, _server, _raw, wants, _float == math.pi)
        self.assert_cmp(client_id, _server, _raw, wants, _string == "a lot of string")
        return {
            "struct": {
                "bool": False,
                "int": 2,
                "float": math.pi,
                "string": "a lot of string",
            }
        }

    def Test07(
        self,
        client_id: str,
        _dict: Dict[str, Any],
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Dict[str, str]]:
        self.assert_method(client_id, _server, "Test07", "Test08")
        wants = {
            "method": "org.varlink.certification.Test07",
            "parameters": {
                "client_id": client_id,
                "struct": {
                    "bool": False,
                    "int": 2,
                    "float": math.pi,
                    "string": "a lot of string",
                },
            },
        }
        self.assert_raw(client_id, _server, _raw, _message, wants)
        self.assert_cmp(client_id, _server, _raw, wants, _dict["int"] == 2)
        self.assert_cmp(client_id, _server, _raw, wants, _dict["bool"] == False)
        self.assert_cmp(client_id, _server, _raw, wants, _dict["float"] == math.pi)
        self.assert_cmp(
            client_id, _server, _raw, wants, _dict["string"] == "a lot of string"
        )
        return {"map": {"foo": "Foo", "bar": "Bar"}}

    def Test08(
        self,
        client_id: str,
        _map: Dict[str, str],
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Set[str]]:
        self.assert_method(client_id, _server, "Test08", "Test09")
        self.assert_raw(
            client_id,
            _server,
            _raw,
            _message,
            {
                "method": "org.varlink.certification.Test08",
                "parameters": {
                    "client_id": client_id,
                    "map": {"foo": "Foo", "bar": "Bar"},
                },
            },
        )
        return {"set": {"one", "two", "three"}}

    def Test09(
        self,
        client_id: str,
        _set: Set[str],
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        self.assert_method(client_id, _server, "Test09", "Test10")
        wants = {
            "method": "org.varlink.certification.Test09",
            "parameters": {
                "client_id": client_id,
                "set": {"one": {}, "three": {}, "two": {}},
            },
        }
        self.assert_raw(client_id, _server, _raw, _message, wants)
        self.assert_cmp(client_id, _server, _raw, wants, isinstance(_set, set))
        self.assert_cmp(client_id, _server, _raw, wants, len(_set) == 3)
        self.assert_cmp(client_id, _server, _raw, wants, "one" in _set)
        self.assert_cmp(client_id, _server, _raw, wants, "two" in _set)
        self.assert_cmp(client_id, _server, _raw, wants, "three" in _set)
        return {
            "client_id": client_id,
            "mytype": {
                "object": {
                    "method": "org.varlink.certification.Test09",
                    "parameters": {"map": {"foo": "Foo", "bar": "Bar"}},
                },
                "enum": "two",
                "struct": {"first": 1, "second": "2"},
                "array": ["one", "two", "three"],
                "dictionary": {"foo": "Foo", "bar": "Bar"},
                "stringset": {"one", "two", "three"},
                "nullable": None,
                "nullable_array_struct": None,
                "interface": {
                    "foo": [
                        None,
                        {"foo": "foo", "bar": "bar"},
                        None,
                        {"one": "foo", "two": "bar"},
                    ],
                    "anon": {"foo": True, "bar": False},
                },
            },
        }

    def Test10(
        self,
        client_id: str,
        mytype: Dict[str, Any],
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Generator[Dict[str, Any], None, None]:
        self.assert_method(client_id, _server, "Test10", "Test11")
        wants = {
            "method": "org.varlink.certification.Test10",
            "more": True,
            "parameters": {
                "client_id": client_id,
                "mytype": {
                    "object": {
                        "method": "org.varlink.certification.Test09",
                        "parameters": {"map": {"foo": "Foo", "bar": "Bar"}},
                    },
                    "enum": "two",
                    "struct": {"first": 1, "second": "2"},
                    "array": ["one", "two", "three"],
                    "dictionary": {"foo": "Foo", "bar": "Bar"},
                    "stringset": {"one", "two", "three"},
                    "interface": {
                        "foo": [
                            None,
                            {"foo": "foo", "bar": "bar"},
                            None,
                            {"one": "foo", "two": "bar"},
                        ],
                        "anon": {"foo": True, "bar": False},
                    },
                },
            },
        }
        if "nullable" in mytype:
            self.assert_cmp(client_id, _server, _raw, wants, mytype["nullable"] == None)
            del mytype["nullable"]
        if "nullable_array_struct" in mytype:
            self.assert_cmp(
                client_id, _server, _raw, wants, mytype["nullable_array_struct"] == None
            )
            del mytype["nullable_array_struct"]
        self.assert_cmp(
            client_id, _server, _raw, wants, mytype == wants["parameters"]["mytype"]
        )
        for i in range(1, 11):
            yield {"string": "Reply number %d" % i, "_continues": i != 10}

    def Test11(
        self,
        client_id: str,
        last_more_replies: List[str],
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
        _oneway: bool = False,
    ) -> None:
        self.assert_method(client_id, _server, "Test11", "End")
        self.assert_cmp(client_id, _server, _raw, wants={}, _bool=_oneway)
        if len(last_more_replies) != 10:
            self.assert_cmp(client_id, _server, _raw, wants={}, _bool=False)
        for i in range(0, 10):
            self.assert_cmp(
                client_id,
                _server,
                _raw,
                {},
                last_more_replies[i] == "Reply number %d" % (i + 1),
            )

    def End(
        self,
        client_id: str,
        _server: Any = None,
        _raw: Optional[bytes] = None,
        _message: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, bool]:
        self.assert_method(client_id, _server, "End", "Start")
        self.assert_raw(
            client_id,
            _server,
            _raw,
            _message,
            {
                "method": "org.varlink.certification.End",
                "parameters": {"client_id": client_id},
            },
        )
        del _server.next_method[client_id]
        return {"all_ok": True}


def run_server(address: str) -> None:
    with varlink.ThreadingServer(address, ServiceRequestHandler) as server:
        print("python-server: listening on", server.server_address)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass


if __name__ == "__main__":
    address = sys.argv[1] if len(sys.argv) > 1 else "unix:@py-interop-echo"
    run_server(address)
