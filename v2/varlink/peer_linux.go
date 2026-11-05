//go:build linux

package varlink

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// peerOf captures the credentials of the process that established conn,
// via SO_PEERCRED and, where the kernel supports it, SO_PEERPIDFD.
func peerOf(conn any) (Peer, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, false
	}

	var peer Peer
	var credErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credErr = err
			return
		}
		peer.UID, peer.GID, peer.PID = int(ucred.Uid), int(ucred.Gid), int(ucred.Pid)

		pidfd, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		if err != nil || pidfd < 0 {
			// SO_PEERPIDFD requires Linux 6.5; its absence is not an error.
			return
		}
		peer.PIDFD = os.NewFile(uintptr(pidfd), "varlink-peer-pidfd")
	})
	if ctrlErr != nil || credErr != nil {
		if peer.PIDFD != nil {
			peer.PIDFD.Close()
		}
		return Peer{}, false
	}
	return peer, true
}
