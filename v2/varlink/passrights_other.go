//go:build !linux

package varlink

import "net"

// disablePassRights is a no-op on platforms without the SO_PASSRIGHTS
// socket option. Descriptors sent by peers are still not accepted: they
// are discarded by the kernel when the connection is read without a
// control-message buffer.
func disablePassRights(ln *net.UnixListener) {}
