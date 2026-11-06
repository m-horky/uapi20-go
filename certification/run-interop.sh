#!/bin/sh
# Runs the full Go/Python varlink interop matrix:
#
#   1. echo battery:  {go,python}-client x {go,python}-server
#   2. certification: go-client -> python-server, python-client -> go-server
#   3. wire-level probes (probe.py) against both servers
#   4. bad-server probes (client strictness) with both clients
#
# Run from anywhere: sh certification/run-interop.sh
# Results are written to a temporary directory (configure by setting OUT=...).
#
# The Python reference implementation is required:
#   python3 -m pip install varlink==31.0.0

set -e
cd "$(dirname "$0")"

OUT_OWNED=0
if [ -z "${OUT:-}" ]; then
    OUT=$(mktemp -d "${TMPDIR:-/tmp}/varlink-certification.XXXXXX")
    OUT_OWNED=1
fi
mkdir -p "$OUT"
GO_SERVER=${GO_SERVER:-$OUT/go-server}
GO_CLIENT=${GO_CLIENT:-$OUT/go-client}

go build -o "$GO_SERVER" ./go-server
go build -o "$GO_CLIENT" ./go-client

GO_PID=
PY_PID=
BAD_PID=

cleanup() {
    for pid in "$GO_PID" "$PY_PID" "$BAD_PID"; do
        if [ -n "$pid" ]; then
            kill "$pid" 2>/dev/null || true
        fi
    done
    wait 2>/dev/null || true
    if [ "$OUT_OWNED" = 1 ]; then
        rm -rf "$OUT"
    fi
}
trap cleanup EXIT INT TERM

start_go_server() {
    "$GO_SERVER" -address "$1" >"$OUT/go-server.log" 2>&1 &
    GO_PID=$!
    sleep 1
}

start_py_server() {
    python3 ./python-server.py "$1" >"$OUT/py-server.log" 2>&1 &
    PY_PID=$!
    sleep 1
}

stop_servers() {
    for pid in "$GO_PID" "$PY_PID"; do
        if [ -n "$pid" ]; then
            kill "$pid" 2>/dev/null || true
        fi
    done
    wait 2>/dev/null || true
    GO_PID=
    PY_PID=
}

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

require_contains() {
    file=$1
    text=$2
    grep -Fq "$text" "$file" || fail "$file does not contain: $text"
}

require_no_match() {
    file=$1
    pattern=$2
    if grep -Eq "$pattern" "$file"; then
        echo "Unexpected output in $file:" >&2
        grep -En "$pattern" "$file" >&2 || true
        fail "$file matches forbidden pattern: $pattern"
    fi
}

require_text() {
    text=$1
    expected=$2
    case $text in
        *"$expected"*) ;;
        *) fail "output does not contain: $expected (got: $text)" ;;
    esac
}

validate_battery() {
    file=$1
    while IFS='|' read -r name expected; do
        if [ -n "$expected" ]; then
            require_contains "$file" "[$name] $expected"
        else
            require_contains "$file" "[$name]"
        fi
    done <<'EOF'
01 GetInfo|vendor="Example"
02 GetInterfaceDescription|description starts
03 Ping|OK pøing <&> ✓
04 Add|OK 42
05 Divide|OK 0.125
06 Toggle|OK false
07 Join null-sep|OK abc
07b Join sep|OK a-b-c
08 Lookup hit|OK {id:7 name:"seven" labels:{lang=en, odd=yes} tags:[odd prime]}
09 Lookup miss|REMOTE com.example.echo.NoSuchRecord {"id":99}
10 Words|OK [brown fox quick the]
11 Choose|OK fast
12 CountMembers|OK 3
13 Fail|REMOTE com.example.echo.TooBusy {"retry_after":3}
14 Notify oneway|OK (follow-up after-oneway)
15 Watch|OK [1 2 3]
16 MethodNotFound|REMOTE org.varlink.service.MethodNotFound
17 InterfaceNotFound|REMOTE org.varlink.service.InterfaceNotFound
18 unknown-parameter|REMOTE org.varlink.service.InvalidParameter
19 wrong-type|REMOTE org.varlink.service.InvalidParameter
20 missing-parameter|REMOTE org.varlink.service.InvalidParameter
21 more-on-unary|OK replies=1 pong="streamed"
22 service-description|description starts
23 batch (Go-only)|
24 fd-passing (Go-only)|
25 no-dot-method|REMOTE
26 upgrade (Go-only)|
EOF
    require_no_match "$file" "ERROR|LOCAL|failed"
}

validate_certification() {
    file=$1
    for step in Start Test01 Test02 Test03 Test04 Test05 Test06 Test07 \
                Test08 Test09 Test10 Test11 End; do
        require_contains "$file" "[cert $step] OK"
    done
    require_contains "$file" "CERTIFICATION PASSED"
    require_no_match "$file" "ERROR|failed"
}

GO_ADDR=unix:@go-interop-echo
PY_ADDR=unix:@py-interop-echo

echo "== 1. echo battery =========================================="
start_go_server "$GO_ADDR"
start_py_server "$PY_ADDR"

"$GO_CLIENT" -address "$GO_ADDR"      | tee "$OUT/battery-go-client--go-server.txt"
"$GO_CLIENT" -address "$PY_ADDR"      | tee "$OUT/battery-go-client--py-server.txt"
python3 ./python-client.py "$GO_ADDR" | tee "$OUT/battery-py-client--go-server.txt"
python3 ./python-client.py "$PY_ADDR" | tee "$OUT/battery-py-client--py-server.txt"

validate_battery "$OUT/battery-go-client--go-server.txt"
validate_battery "$OUT/battery-go-client--py-server.txt"
validate_battery "$OUT/battery-py-client--go-server.txt"
validate_battery "$OUT/battery-py-client--py-server.txt"

echo
echo "== 2. certification ========================================"
"$GO_CLIENT" -address "$PY_ADDR" -certify | tee "$OUT/cert-go-client--py-server.txt"
python3 ./python-client.py "$GO_ADDR" --certify | tee "$OUT/cert-py-client--go-server.txt"

validate_certification "$OUT/cert-go-client--py-server.txt"
validate_certification "$OUT/cert-py-client--go-server.txt"

stop_servers

echo
echo "== 3. wire-level probes ===================================="
start_go_server "$GO_ADDR"
python3 ./probe.py "$GO_ADDR" | tee "$OUT/probe-go-server.txt"
stop_servers

# These are MUST-level framing rules from the UAPI specification. The
# reference Python server is known to be lax on several of them, so only the
# Go server's probe results are asserted here.
for expected in \
    "[P1 invalid-json] CLOSED" \
    "[P2 non-object] CLOSED" \
    "[P3 unknown-member] CLOSED" \
    "[P4 two-flags] CLOSED" \
    "[P5 params-array] CLOSED" \
    "[P7 client-error-msg] CLOSED" \
    "[P8 continues-in-call] CLOSED" \
    "[P10 empty-frame] CLOSED" \
    "[P15 flag-not-bool] CLOSED" \
    "[P18 method-not-string] CLOSED" \
    "[P16 bridge-no-upgrade] REPLY" \
    "[P17 bridge-upgrade] REPLY {} | RAW-REPLY"
do
    require_contains "$OUT/probe-go-server.txt" "$expected"
done

start_py_server "$PY_ADDR"
python3 ./probe.py "$PY_ADDR" | tee "$OUT/probe-py-server.txt"
stop_servers

echo
echo "== 4. bad-server probes (client strictness) ================"
: >"$OUT/bad-server-clients.txt"
for mode in "B1 reply-with-method" "B2 continues-on-unary" "B3 unknown-member" \
            "B4 non-object" "B5 error-continues"; do
    python3 ./bad-server.py unix:@bad-interop "$mode" >"$OUT/bad-server.log" 2>&1 &
    BAD_PID=$!
    sleep 1
    go_out=$("$GO_CLIENT" -address unix:@bad-interop -single)
    py_out=$(python3 ./python-single.py unix:@bad-interop)
    printf '%s\n' "--- $mode" "go-client:     $go_out" \
        "python-client: $py_out" | tee -a "$OUT/bad-server-clients.txt"

    case $mode in
        "B1 reply-with-method")
            go_expected="[single Ping] LOCAL"
            py_expected="[single Ping] OK x"
            ;;
        "B2 continues-on-unary")
            go_expected="[single Ping] LOCAL"
            py_expected="[single Ping] LOCAL"
            ;;
        "B3 unknown-member")
            go_expected="[single Ping] LOCAL"
            py_expected="[single Ping] OK None"
            ;;
        "B4 non-object")
            go_expected="[single Ping] LOCAL"
            py_expected="[single Ping] LOCAL TypeError"
            ;;
        "B5 error-continues")
            go_expected="[single Ping] LOCAL"
            py_expected="[single Ping] REMOTE com.example.echo.Bad"
            ;;
        *)
            fail "no expectations for $mode"
            ;;
    esac
    require_text "$go_out" "$go_expected"
    require_text "$py_out" "$py_expected"

    kill "$BAD_PID" 2>/dev/null || true
    wait "$BAD_PID" 2>/dev/null || true
    BAD_PID=
done

echo
if [ "$OUT_OWNED" = 1 ]; then
    echo "Done. Temporary results removed; set OUT=... to keep them."
else
    echo "Done. Results in $OUT"
fi
