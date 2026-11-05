//go:build linux

package varlink

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// tagSocketFD sets the user.varlink attribute on the socket behind a
// syscall connection. The connection must be idle (no reads, writes, or
// accepts in flight), so that Control can run.
func tagSocketFD(conn syscall.Conn, role string) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = unix.Fsetxattr(int(fd), XattrUserVarlink, []byte(role), 0)
	})
}

// tagPath sets the user.varlink attribute on the file-system inode at path.
// Best-effort; failures are ignored.
func tagPath(path, role string) {
	_ = unix.Setxattr(path, XattrUserVarlink, []byte(role), 0)
}

// tagUnixListener tags a path-bound listener with both the listen role (on
// the sockfs inode) and the entrypoint role (on the file-system inode).
func tagUnixListener(ln net.Listener, path string) {
	if ul, ok := ln.(*net.UnixListener); ok {
		tagSocketFD(ul, RoleListen)
	}
	if path != "" && path[0] != '@' {
		tagPath(path, RoleEntrypoint)
	}
}

// tagUnixConn tags a connection socket with the given role (client or
// server). Best-effort.
func tagUnixConn(conn *net.UnixConn, role string) {
	tagSocketFD(conn, role)
}

// tagServerConn tags the serving end of an accepted connection.
func tagServerConn(conn any) {
	if uc, ok := conn.(*net.UnixConn); ok {
		tagUnixConn(uc, RoleServer)
	}
}

// tagClientConn tags the connecting end of a connection.
func tagClientConn(conn any) {
	if uc, ok := conn.(*net.UnixConn); ok {
		tagUnixConn(uc, RoleClient)
	}
}
