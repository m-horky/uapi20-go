//go:build !linux

package varlink

// peerOf has no implementation on this platform: the transports available
// here do not carry peer credentials.
func peerOf(conn any) (Peer, bool) {
	return Peer{}, false
}
