//go:build unix

package varlink

import (
	"bytes"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// maxSCMFileDescriptors matches the kernel limit of descriptors deliverable
// in a single SCM_RIGHTS control message.
const maxSCMFileDescriptors = 253

var _ FDConn = (*fdConn)(nil)

// fdConn adapts a stream *net.UnixConn for the varlink runtime while
// tracking SCM_RIGHTS ancillary data received with the byte stream.
//
// File descriptors are associated with the message whose first byte they
// arrive with: the kernel delivers the control message of a sendmsg(2) call
// with the first byte of its data, and compliant senders attach descriptors
// only to the sendmsg transmitting the first byte of the message they
// belong to. fdConn therefore scans the received byte stream for message
// boundaries (NUL-terminated frames) and queues the descriptors of each
// completed message, in order, for the consumer to take one message at a
// time. Descriptors arriving with a chunk of data that neither starts nor
// contains the start of a new message are not accepted: they are closed
// immediately.
type fdConn struct {
	conn   *net.UnixConn
	accept bool

	// oob is the reusable buffer for control messages. It is only used
	// while holding mu, on the single-reader goroutine.
	oob []byte

	mu         sync.Mutex
	raw        bool       // detached (upgraded protocol) mode
	midMessage bool       // stream position is inside an unfinished message
	curFDs     []*os.File // descriptors of the message currently arriving
	carry      []*os.File // descriptors for the next message to start
	queue      [][]*os.File
}

// wrapFDConn returns an fdConn for conn if it is an AF_UNIX stream
// connection, or nil otherwise. accept controls whether SCM_RIGHTS control
// data is received; when it is false, the kernel discards any descriptors
// a peer sends.
func wrapFDConn(conn io.ReadWriteCloser, accept bool) *fdConn {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	return &fdConn{
		conn:   uc,
		accept: accept,
		oob:    make([]byte, unix.CmsgSpace(maxSCMFileDescriptors*4)),
	}
}

// Read reads data from the connection. In framed mode the byte stream is
// scanned for message boundaries so that received descriptors stay
// associated with the message they belong to.
func (c *fdConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.raw {
			return c.conn.Read(p)
		}
		n, err := c.recv(p)
		if n > 0 {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// recv performs one ReadMsgUnix and updates the descriptor association
// state for the data it returns.
func (c *fdConn) recv(p []byte) (int, error) {
	var oob []byte
	if c.accept {
		oob = c.oob
	}
	n, oobn, flags, _, err := c.conn.ReadMsgUnix(p, oob)
	// On error, the raw recvmsg results are not zeroed (see the TODO in
	// internal/poll), so negative lengths must not reach the slicing below.
	if n <= 0 {
		return 0, err
	}
	oobn = min(max(oobn, 0), len(oob))
	if oobn == 0 && flags&unix.MSG_CTRUNC == 0 {
		c.scanChunk(p[:n], nil)
		return n, err
	}
	files, parseErr := parseRights(c.oob[:oobn])
	if parseErr != nil || flags&unix.MSG_CTRUNC != 0 {
		// The control data is truncated or malformed; the descriptors
		// cannot be associated safely. Drop them and fail the
		// connection.
		closeFiles(files)
		c.dropQueued()
		return 0, &malformedControlMessage{}
	}
	c.scanChunk(p[:n], files)
	return n, err
}

// scanChunk updates the message-boundary and descriptor-association state
// for one chunk of received data.
func (c *fdConn) scanChunk(data []byte, files []*os.File) {
	atStart := !c.midMessage
	if atStart {
		if len(c.carry) > 0 || len(files) > 0 {
			c.curFDs = append(c.carry, files...)
			c.carry = nil
		}
	} else if bytes.IndexByte(data, 0) >= 0 {
		// The chunk completes the current message and contains the start
		// of a new one; the descriptors belong to that new message.
		c.carry = append(c.carry, files...)
	} else if len(files) > 0 {
		// Descriptors arrived with a chunk that neither starts nor
		// contains the start of a new message. The sender violated the
		// protocol; the descriptors must not be accepted.
		closeFiles(files)
	}

	c.midMessage = true
	off := 0
	for {
		i := bytes.IndexByte(data[off:], 0)
		if i < 0 {
			break
		}
		// The message starting at off is complete; its descriptors are
		// curFDs (assigned when it started).
		c.queue = append(c.queue, c.curFDs)
		c.curFDs = nil
		off += i + 1
		if off < len(data) {
			if len(c.carry) > 0 {
				c.curFDs = c.carry
				c.carry = nil
			}
		} else {
			c.midMessage = false
		}
	}
}

// takeMessageFDs returns the descriptors received with the message most
// recently read by the consumer, in message order.
func (c *fdConn) takeMessageFDs() []*os.File {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return nil
	}
	files := c.queue[0]
	c.queue = c.queue[1:]
	return files
}

// dropQueued closes every descriptor held for pending messages.
func (c *fdConn) dropQueued() {
	closeFiles(c.curFDs)
	closeFiles(c.carry)
	c.curFDs = nil
	c.carry = nil
	for _, files := range c.queue {
		closeFiles(files)
	}
	c.queue = nil
}

// detachFD switches the connection to raw mode for an upgraded protocol.
// Descriptors held for messages of the framed phase are surfaced by
// ReadMsg.
func (c *fdConn) detachFD() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raw = true
	if len(c.curFDs) > 0 {
		c.queue = append(c.queue, c.curFDs)
		c.curFDs = nil
	}
	if len(c.carry) > 0 {
		c.queue = append(c.queue, c.carry)
		c.carry = nil
	}
}

// Write writes p to the connection.
func (c *fdConn) Write(p []byte) (int, error) {
	return c.conn.Write(p)
}

// WriteMsg writes p as one chunk, attaching files in an SCM_RIGHTS
// ancillary message.
func (c *fdConn) WriteMsg(p []byte, files []*os.File) error {
	if len(files) == 0 {
		_, err := c.conn.Write(p)
		return err
	}
	fds := make([]int, len(files))
	for i, f := range files {
		if f == nil {
			return ErrFileDescriptorsUnsupported
		}
		fds[i] = int(f.Fd())
	}
	_, _, err := c.conn.WriteMsgUnix(p, unix.UnixRights(fds...), nil)
	return err
}

// writeFrame writes one NUL-terminated frame with its descriptors attached
// to the first byte, in a single sendmsg call.
func (c *fdConn) writeFrame(frame []byte, files []*os.File) error {
	buf := make([]byte, len(frame)+1)
	copy(buf, frame)
	buf[len(buf)-1] = 0
	return c.WriteMsg(buf, files)
}

// ReadMsg reads up to len(p) bytes and returns the file descriptors
// received with them.
func (c *fdConn) ReadMsg(p []byte) (int, []*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Surface descriptors still held from the framed phase first, so that
	// they are not lost when the connection is detached mid-stream.
	var queued []*os.File
	for _, files := range c.queue {
		queued = append(queued, files...)
	}
	c.queue = nil

	n, oobn, flags, _, err := c.conn.ReadMsgUnix(p, c.oob)
	oobn = min(max(oobn, 0), len(c.oob))
	files, parseErr := parseRights(c.oob[:oobn])
	if parseErr != nil || flags&unix.MSG_CTRUNC != 0 {
		closeFiles(files)
		closeFiles(queued)
		if err == nil {
			err = &malformedControlMessage{}
		}
		return n, nil, err
	}
	return n, append(queued, files...), err
}

// Close closes the connection and every descriptor still held for pending
// messages.
func (c *fdConn) Close() error {
	c.mu.Lock()
	pending := append(append(c.curFDs, c.carry...), flattenFDs(c.queue)...)
	c.curFDs, c.carry, c.queue = nil, nil, nil
	c.mu.Unlock()
	closeFiles(pending)
	return c.conn.Close()
}

func flattenFDs(queue [][]*os.File) []*os.File {
	var out []*os.File
	for _, files := range queue {
		out = append(out, files...)
	}
	return out
}

// parseRights extracts the file descriptors of all SCM_RIGHTS control
// messages in oob, wrapping them in *os.File.
func parseRights(oob []byte) ([]*os.File, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	var files []*os.File
	for _, m := range msgs {
		if m.Header.Type != unix.SCM_RIGHTS {
			continue
		}
		fds, err := unix.ParseUnixRights(&m)
		if err != nil {
			return files, err
		}
		for _, fd := range fds {
			if fd < 0 {
				continue
			}
			files = append(files, os.NewFile(uintptr(fd), "varlink-fd"))
		}
	}
	return files, nil
}

// LocalAddr returns the local network address.
func (c *fdConn) LocalAddr() net.Addr { return c.conn.LocalAddr() }

// RemoteAddr returns the remote network address.
func (c *fdConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// SetDeadline sets the read and write deadlines.
func (c *fdConn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

// SetReadDeadline sets the read deadline.
func (c *fdConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// SetWriteDeadline sets the write deadline.
func (c *fdConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
