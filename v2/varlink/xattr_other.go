//go:build !linux

package varlink

// Extended attributes on socket inodes are a Linux facility; on other
// platforms tagging is a no-op.

func tagPath(path, role string) {}

func tagUnixListener(ln any, path string) {}

func tagServerConn(conn any) {}

func tagClientConn(conn any) {}
