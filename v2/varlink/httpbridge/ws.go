package httpbridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/varlink/go/v2/varlink"

	"golang.org/x/net/websocket"
)

// binaryCodec sends and receives WebSocket binary frames verbatim.
var binaryCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) {
		payload, ok := v.([]byte)
		if !ok {
			return nil, websocket.BinaryFrame, errors.New("httpbridge: frame must be []byte")
		}
		return payload, websocket.BinaryFrame, nil
	},
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		return nil
	},
}

// handleWebSocket serves the WebSocket mode: after a successful upgrade, it
// connects to the service's socket and acts as a transparent byte bridge
// between the WebSocket and the varlink connection, in both directions.
func (b *Bridge) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	address, ok := b.lookupService(w, r.PathValue("service"))
	if !ok {
		return
	}
	// The bridge must not accept file descriptors from the service; the
	// raw connection reads with read(2), which makes the kernel discard
	// any SCM_RIGHTS ancillary data.
	conn, err := varlink.DialConn(r.Context(), address)
	if err != nil {
		// If the bridge cannot connect to the service's socket, the
		// HTTP upgrade request fails and no WebSocket connection is
		// established.
		writeMessage(w, http.StatusBadGateway, err.Error())
		return
	}

	// The handshake refuses cross-origin requests: browser clients send an
	// Origin header and cannot be authorized by the HTTP layer, so only
	// requests whose origin matches the bridge's own host are accepted.
	// Requests without an Origin header (non-browser clients) are allowed.
	server := websocket.Server{
		Handler: func(ws *websocket.Conn) {
			pumpWebSocket(ws, conn)
		},
		Handshake: checkWebSocketOrigin,
	}
	server.ServeHTTP(w, r)
}

// checkWebSocketOrigin rejects WebSocket handshakes whose Origin, when
// present, does not match the host the request was addressed to.
func checkWebSocketOrigin(config *websocket.Config, req *http.Request) error {
	origin := req.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("httpbridge: invalid origin %q", origin)
	}
	if u.Host != req.Host {
		return fmt.Errorf("httpbridge: cross-origin websocket request from %q", origin)
	}
	return nil
}

// pumpWebSocket bridges the WebSocket and the varlink connection until
// either side closes.
//
// Until a protocol upgrade is confirmed, the service-to-client direction is
// re-framed at varlink message boundaries: each message, including its
// terminating NUL, is sent as one binary frame. Client-to-service frames
// (text or binary alike) are concatenated into the byte stream. Once an
// upgrade call has been confirmed by the service, both directions forward
// bytes verbatim, in frames of arbitrary size, since the bridge can no
// longer make assumptions about the payload.
func pumpWebSocket(ws *websocket.Conn, service net.Conn) {
	defer ws.Close()
	defer service.Close()

	tracker := &upgradeTracker{}
	confirmed := make(chan struct{})
	var once sync.Once
	confirm := func() { once.Do(func() { close(confirmed) }) }

	// Client to service.
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := ws.Read(buf)
			if n > 0 {
				// Text frames MUST also be accepted and are
				// treated identically to binary ones; the
				// stream reader does not distinguish them.
				tracker.feed(buf[:n])
				if _, werr := service.Write(buf[:n]); werr != nil {
					confirm()
					return
				}
			}
			if err != nil {
				confirm()
				return
			}
			select {
			case <-confirmed:
				// From here on the payload is not varlink
				// anymore; only forward.
				tracker.stop()
			default:
			}
		}
	}()

	// Service to client.
	reader := bufio.NewReader(service)
	for {
		message, err := readNULMessage(reader)
		if err != nil {
			return
		}
		if tracker.replyConfirmsUpgrade(message) {
			// Forward the confirming reply, then stop re-framing.
			if sendFrame(ws, message) != nil {
				return
			}
			confirm()
			rawCopy(ws, reader)
			return
		}
		if sendFrame(ws, message) != nil {
			return
		}
	}
}

// rawCopy forwards the remaining bytes of the upgraded protocol verbatim,
// in frames of arbitrary size.
func rawCopy(ws *websocket.Conn, reader *bufio.Reader) {
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if sendFrame(ws, buf[:n]) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// sendFrame sends one binary frame.
func sendFrame(ws *websocket.Conn, payload []byte) error {
	return binaryCodec.Send(ws, payload)
}

// readNULMessage reads one NUL-terminated message, including the NUL, so
// that messages are re-framed with their terminator as one frame.
func readNULMessage(reader *bufio.Reader) ([]byte, error) {
	var message []byte
	for {
		chunk, err := reader.ReadSlice(0)
		message = append(message, chunk...)
		if err == nil {
			return message, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(message) > 0 && errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
}

// upgradeTracker watches the byte stream from client to service for calls
// with the upgrade flag set, and matches them against the replies from the
// service, so that the bridge can detect a confirmed protocol upgrade.
// Replies arrive in call order; a reply completes the oldest outstanding
// call that expects a reply.
type upgradeTracker struct {
	mu      sync.Mutex
	stopped bool
	partial []byte // bytes of an incomplete message
	pending []bool // outstanding calls, oldest first
}

// feed consumes client-to-service bytes.
func (t *upgradeTracker) feed(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.partial = append(t.partial, data...)
	for {
		i := indexNUL(t.partial)
		if i < 0 {
			return
		}
		message := t.partial[:i]
		rest := t.partial[i+1:]

		var call struct {
			Oneway  bool `json:"oneway"`
			Upgrade bool `json:"upgrade"`
		}
		// The bridge must not assume a frame contains exactly one
		// message, nor that a message is valid JSON; unparsable
		// messages are simply not tracked.
		if json.Unmarshal(message, &call) == nil && !call.Oneway {
			t.pending = append(t.pending, call.Upgrade)
		}

		t.partial = append([]byte(nil), rest...)
	}
}

// stop stops tracking; the payload is no longer varlink.
func (t *upgradeTracker) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
}

// replyConfirmsUpgrade reports whether the reply message confirms a call
// that requested a protocol upgrade. A reply completes the oldest
// outstanding call; error replies reject it.
func (t *upgradeTracker) replyConfirmsUpgrade(reply []byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || len(t.pending) == 0 {
		return false
	}
	reply = bytes.TrimSuffix(reply, []byte{0})
	var message struct {
		Error     string `json:"error"`
		Continues bool   `json:"continues"`
	}
	if json.Unmarshal(reply, &message) != nil {
		return false
	}
	if message.Error == "" && message.Continues {
		// A streaming reply does not complete the call.
		return false
	}
	upgrade := t.pending[0]
	t.pending = t.pending[1:]
	return upgrade && message.Error == ""
}

func indexNUL(data []byte) int {
	for i, b := range data {
		if b == 0 {
			return i
		}
	}
	return -1
}
