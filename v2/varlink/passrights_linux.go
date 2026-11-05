//go:build linux

package varlink

import (
	"net"

	"golang.org/x/sys/unix"
)

// disablePassRights turns off reception of file descriptors on a listening
// socket (SO_PASSRIGHTS, available since Linux 6.16), so that descriptors
// sent by misbehaving peers are discarded by the kernel instead of being
// queued. Best-effort: kernels without the option keep their default
// behavior, in which descriptors are dropped when the connection is read
// without a control-message buffer.
func disablePassRights(ln *net.UnixListener) {
	if ln == nil {
		return
	}
	raw, err := ln.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSRIGHTS, 0)
	})
}
