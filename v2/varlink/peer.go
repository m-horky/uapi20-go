package varlink

import (
	"context"
	"os"
)

// Peer holds the credentials of the process that established a connection,
// as captured by the server when the connection was accepted.
//
// Peer credentials are properties of the connection, not of individual
// messages. Servers capture them once per connection where the transport
// supports it (SO_PEERCRED and SO_PEERPIDFD on AF_UNIX sockets on Linux);
// on other transports there is no peer information and PeerFromContext
// reports false.
type Peer struct {
	// UID, GID and PID identify the peer process at the time of connect.
	// They are only set when the transport provides them.
	UID int
	GID int
	PID int
	// PIDFD is a pidfd referring to the peer process at the time of
	// connect (Linux 6.5 and later), or nil when unavailable. Unlike PID,
	// it is safe against PID recycling. It is owned by the connection and
	// closed when the connection ends; handlers that need to keep it
	// beyond that must duplicate the underlying descriptor.
	PIDFD *os.File
}

type peerContextKey struct{}

// PeerFromContext returns the peer credentials of the connection the call
// was received on. The second return value is false when there is no peer
// information, e.g. on transports without credential support.
func PeerFromContext(ctx context.Context) (Peer, bool) {
	peer, ok := ctx.Value(peerContextKey{}).(Peer)
	return peer, ok
}

func withPeer(ctx context.Context, peer Peer) context.Context {
	return context.WithValue(ctx, peerContextKey{}, peer)
}
