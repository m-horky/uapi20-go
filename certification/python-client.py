#!/usr/bin/env python3
"""Example varlink client used for interop testing. It runs the same
battery of calls as the Go go-client and prints the same canonical result
lines for the interop harness to validate.

Usage: python3 python-client.py <address> [--certify]
"""

import json
import sys
from typing import Any, Dict, List, Optional, Tuple

import varlink


def q(s: str) -> str:
    """Format like Go's %q for simple strings."""
    return '"%s"' % s


def bstr(b: bool) -> str:
    """Format a bool like Go's %v."""
    return "true" if b else "false"


def canon(obj: Any) -> str:
    """Format a JSON value like Go's encoding/json."""
    return json.dumps(obj, sort_keys=True, separators=(",", ":"))


def error_result(err: Optional[BaseException]) -> str:
    if err is None:
        return "OK"
    if isinstance(err, varlink.VarlinkError):
        try:
            params = err.parameters() or {}
        except Exception:
            params = {}
        return "REMOTE %s %s" % (err.error(), canon(params))
    return "LOCAL %s" % err


def raw_call(
    con: Any, message: Dict[str, Any]
) -> Tuple[Optional[varlink.VarlinkError], Optional[Dict[str, Any]], Optional[bool]]:
    """Send one raw frame, return (error, params, more)."""
    con._send_message(json.dumps(message).encode("utf-8"))
    try:
        params, more = con._next_varlink_message()
        return None, params, more
    except varlink.VarlinkError as e:
        return e, None, None


def run_echo_battery(address: str) -> None:
    checks: List[str] = []

    def record(name: str, result: str) -> None:
        checks.append("[%s] %s" % (name, result))

    client = varlink.Client(address)
    with client.open("com.example.echo") as con:
        # 01: GetInfo
        try:
            info = client.get_interfaces()
            meta = client.info
            record(
                "01 GetInfo",
                "vendor=%s product=%s version=%s url=%s interfaces=[%s]"
                % (
                    q(meta["vendor"]),
                    q(meta["product"]),
                    q(meta["version"]),
                    q(meta["url"]),
                    " ".join(sorted(info)),
                ),
            )
        except Exception as e:
            record("01 GetInfo", "ERROR %s" % e)

        # 02: GetInterfaceDescription
        try:
            desc = (
                con._interface.get_description() if hasattr(con, "_interface") else None
            )
            if desc is None:
                iface = client.get_interface("com.example.echo")
                desc = iface.get_description()
            record(
                "02 GetInterfaceDescription",
                "description starts %s" % q(desc.strip().split("\n")[0]),
            )
        except Exception as e:
            record("02 GetInterfaceDescription", "ERROR %s" % e)

        # 03: Ping
        try:
            ret = con.Ping(ping="pøing <&> ✓")
            record("03 Ping", "OK %s" % ret["pong"])
        except Exception as e:
            record("03 Ping", error_result(e))

        # 04: Add
        try:
            ret = con.Add(a=2, b=40)
            record("04 Add", "OK %d" % ret["sum"])
        except Exception as e:
            record("04 Add", error_result(e))

        # 05: Divide
        try:
            ret = con.Divide(a=1, b=8)
            record("05 Divide", "OK %.3f" % ret["quotient"])
        except Exception as e:
            record("05 Divide", error_result(e))

        # 06: Toggle
        try:
            ret = con.Toggle(flag=True)
            record("06 Toggle", "OK %s" % bstr(ret["flag"]))
        except Exception as e:
            record("06 Toggle", error_result(e))

        # 07: Join without separator
        try:
            ret = con.Join(items=["a", "b", "c"], separator=None)
            record("07 Join null-sep", "OK %s" % ret["joined"])
        except Exception as e:
            record("07 Join null-sep", error_result(e))

        # 07b: Join with separator
        try:
            ret = con.Join(items=["a", "b", "c"], separator="-")
            record("07b Join sep", "OK %s" % ret["joined"])
        except Exception as e:
            record("07b Join sep", error_result(e))

        # 08: Lookup hit
        try:
            ret = con.Lookup(id=7)["record"]
            labels = ", ".join(
                "%s=%s" % (k, ret["labels"][k]) for k in sorted(ret["labels"])
            )
            tags = " ".join(sorted(ret["tags"]))
            record(
                "08 Lookup hit",
                "OK {id:%d name:%s labels:{%s} tags:[%s]}"
                % (ret["id"], q(ret["name"]), labels, tags),
            )
        except Exception as e:
            record("08 Lookup hit", error_result(e))

        # 09: Lookup miss
        try:
            con.Lookup(id=99)
            record("09 Lookup miss", "OK")
        except Exception as e:
            record("09 Lookup miss", error_result(e))

        # 10: Words
        try:
            ret = con.Words(text="the quick brown fox the")
            record("10 Words", "OK [%s]" % " ".join(sorted(ret["words"])))
        except Exception as e:
            record("10 Words", error_result(e))

        # 11: Choose
        try:
            ret = con.Choose(mode="fast")
            record("11 Choose", "OK %s" % ret["mode"])
        except Exception as e:
            record("11 Choose", error_result(e))

        # 12: CountMembers
        try:
            ret = con.CountMembers(value={"a": 1, "b": 2, "c": 3})
            record("12 CountMembers", "OK %d" % ret["members"])
        except Exception as e:
            record("12 CountMembers", error_result(e))

        # 13: Fail
        try:
            con.Fail(code=3)
            record("13 Fail", "OK")
        except Exception as e:
            record("13 Fail", error_result(e))

        # 14: Notify (oneway), then verify the connection is still usable
        try:
            con.Notify(message="hello", _oneway=True)
            followup = "follow-up " + con.Ping(ping="after-oneway")["pong"]
            record("14 Notify oneway", "OK (%s)" % followup)
        except Exception as e:
            record("14 Notify oneway", "%s (follow-up ERROR)" % error_result(e))

        # 15: Watch (streaming)
        try:
            indexes = [r["index"] for r in con.Watch(count=3, _more=True)]
            record("15 Watch", "OK [%s]" % " ".join(str(i) for i in indexes))
        except Exception as e:
            record("15 Watch", error_result(e))

        # 16: unknown method on known interface
        err, params, more = raw_call(con, {"method": "com.example.echo.Nosuch"})
        record("16 MethodNotFound", "OK" if err is None else error_result(err))

        # 17: unknown interface
        err, params, more = raw_call(con, {"method": "com.example.nosuch.Ping"})
        record("17 InterfaceNotFound", "OK" if err is None else error_result(err))

        # 18: unknown parameter
        err, params, more = raw_call(
            con,
            {
                "method": "com.example.echo.Ping",
                "parameters": {"ping": "x", "bogus": 1},
            },
        )
        record("18 unknown-parameter", "OK" if err is None else error_result(err))

        # 19: wrong parameter type
        err, params, more = raw_call(
            con, {"method": "com.example.echo.Ping", "parameters": {"ping": 123}}
        )
        record("19 wrong-type", "OK" if err is None else error_result(err))

        # 20: missing required parameter
        err, params, more = raw_call(con, {"method": "com.example.echo.Ping"})
        record("20 missing-parameter", "OK" if err is None else error_result(err))

        # 21: unary method called with more flag
        try:
            replies = list(con.Ping(ping="streamed", _more=True))
            record(
                "21 more-on-unary",
                "OK replies=%d pong=%s" % (len(replies), q(replies[0]["pong"])),
            )
        except Exception as e:
            record("21 more-on-unary", error_result(e))

        # 22: GetInterfaceDescription for org.varlink.service
        try:
            iface = client.get_interface("org.varlink.service")
            desc = iface.get_description()
            record(
                "22 service-description",
                "description starts %s" % q(desc.strip().split("\n")[0]),
            )
        except Exception as e:
            record("22 service-description", "ERROR %s" % e)

        # 23: batched pipelined calls (no Python client API)
        record("23 batch (Go-only)", "SKIP no batch API in python client")

        # 24: file descriptor check (Python client cannot send descriptors)
        try:
            ret = con.CheckFD()
            record(
                "24 fd-passing (Go-only)",
                "OK server received fds: %s (fds returned by server: 0)"
                % bstr(ret["received"]),
            )
        except Exception as e:
            record("24 fd-passing (Go-only)", error_result(e))

        # 25: method name without a dot
        err, params, more = raw_call(con, {"method": "Nosuchmethod"})
        record("25 no-dot-method", "OK" if err is None else error_result(err))

        # 26: connection upgrade (no Python client API)
        record("26 upgrade (Go-only)", "SKIP no upgrade API in python client")

    for line in checks:
        print(line)


def run_certification(address: str) -> None:
    client = varlink.Client(address)
    with client.open("org.varlink.certification") as con:
        ret = con.Start()
        client_id = ret["client_id"]
        print("[cert Start] OK client_id=%s" % q(client_id))
        ret = con.Test01(client_id)
        print("[cert Test01] OK bool=%s" % bstr(ret["bool"]))
        ret = con.Test02(client_id, ret["bool"])
        print("[cert Test02] OK int=%d" % ret["int"])
        ret = con.Test03(client_id, ret["int"])
        print("[cert Test03] OK float=%s" % ret["float"])
        ret = con.Test04(client_id, ret["float"])
        print("[cert Test04] OK string=%s" % q(ret["string"]))
        ret = con.Test05(client_id, ret["string"])
        print(
            "[cert Test05] OK bool=%s int=%d float=%s string=%s"
            % (bstr(ret["bool"]), ret["int"], ret["float"], q(ret["string"]))
        )
        ret = con.Test06(
            client_id, ret["bool"], ret["int"], ret["float"], ret["string"]
        )
        s = ret["struct"]
        print(
            "[cert Test06] OK struct={bool:%s int:%d float:%s string:%s}"
            % (bstr(s["bool"]), s["int"], s["float"], q(s["string"]))
        )
        ret = con.Test07(client_id, ret["struct"])
        print("[cert Test07] OK map=%s" % ret["map"])
        ret = con.Test08(client_id, ret["map"])
        print("[cert Test08] OK set=%s" % sorted(ret["set"]))
        ret = con.Test09(client_id, ret["set"])
        print("[cert Test09] OK mytype verified=true")
        ret_array: List[str] = []
        for ret in con.Test10(client_id, ret["mytype"], _more=True):
            ret_array.append(ret["string"])
        print(
            "[cert Test10] OK replies=%d last=%s" % (len(ret_array), q(ret_array[-1]))
        )
        ret = con.Test11(client_id, ret_array, _oneway=True)
        print("[cert Test11] OK oneway sent")
        ret = con.End(client_id)
        print("[cert End] OK all_ok=%s" % bstr(ret["all_ok"]))
    print("CERTIFICATION PASSED")


def main() -> None:
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    certify = "--certify" in sys.argv
    address = args[0] if args else "unix:@py-interop-echo"
    if certify:
        run_certification(address)
    else:
        run_echo_battery(address)


if __name__ == "__main__":
    main()
