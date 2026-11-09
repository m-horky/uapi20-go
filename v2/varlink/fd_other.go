//go:build !unix

package varlink

import (
	"io"
)

// fdConn is the no-op stand-in used on platforms without AF_UNIX ancillary
// data support. wrapFDConn always returns nil, so its methods are never
// called; they exist to keep the transport code buildable.
type fdConn struct{}

var _ FDConn = (*fdConn)(nil)

func wrapFDConn(conn io.ReadWriteCloser, accept bool) *fdConn {
	return nil
}

func (c *fdConn) Read(p []byte) (int, error)           { return 0, io.ErrClosedPipe }
func (c *fdConn) Write(p []byte) (int, error)          { return 0, io.ErrClosedPipe }
func (c *fdConn) Close() error                         { return io.ErrClosedPipe }
func (c *fdConn) WriteMsg(p []byte, files Files) error { return ErrFileDescriptorsUnsupported }
func (c *fdConn) ReadMsg(p []byte) (int, Files, error) {
	return 0, nil, ErrFileDescriptorsUnsupported
}
func (c *fdConn) takeMessageFDs() Files { return nil }
func (c *fdConn) writeFrame(frame []byte, files Files) error {
	return ErrFileDescriptorsUnsupported
}
func (c *fdConn) detachFD() {}
