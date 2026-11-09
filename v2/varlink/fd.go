package varlink

import (
	"errors"
	"io"
	"os"
)

// maxSCMFileDescriptors matches the kernel limit of descriptors deliverable
// in a single SCM_RIGHTS control message.
const maxSCMFileDescriptors = 253

// ErrFileDescriptorsUnsupported is returned when file descriptor passing
// is used on a transport that cannot carry file descriptors. Varlink is
// encapsulatable in transports other than AF_UNIX stream sockets, and file
// descriptors cannot be forwarded across such encapsulations; interfaces
// making use of file descriptor passing should document how their methods
// fail over such transports.
var ErrFileDescriptorsUnsupported = errors.New("varlink: file descriptor passing not supported by transport")

// Files is the list of file descriptors passed with one message, in either
// direction. The JSON parameters of the message reference the descriptors
// by their index in this list; which parameter fields carry indices is
// defined by the interface, not by the transport.
//
// Ownership follows zlink's model: the fd list is a per-message argument
// or return value. A Files value received from the API (stored by
// ReceiveFiles, or by a call's Files method) belongs to the caller, which
// must close the descriptors it does not keep; Close closes all of them. A
// Files value passed to the API (SendFiles, PushFile, PushFiles) is
// duplicated by the kernel when the message carrying it is written, so
// the caller may close its copies once that message has been written:
// once the call returns outside a batch (or once Batch.Send has written
// the requests inside one), or, on the server, once the reply carrying
// them has been written.
type Files []*os.File

// Close closes all non-nil files, ignoring errors. It is used to discard
// file descriptors that were received but not consumed.
func (f Files) Close() {
	for _, file := range f {
		if file != nil {
			_ = file.Close()
		}
	}
}

// FDConn is the file-descriptor-capable view of a connection. It is
// implemented by connections on AF_UNIX stream sockets, including the
// connections returned by Client.Upgrade, and can be obtained via a type
// assertion. Transports and encapsulations that cannot carry file
// descriptors do not implement it.
//
// Descriptors are passed in SCM_RIGHTS ancillary messages attached to the
// sendmsg(2) call transmitting the bytes they belong to, per the UAPI
// specification. Receiving descriptors remains subject to the opt-in
// configured through ClientConfig.AcceptFileDescriptors or
// ServerConfig.AcceptFileDescriptors.
type FDConn interface {
	io.ReadWriteCloser

	// WriteMsg writes p as one chunk, attaching files in an SCM_RIGHTS
	// ancillary message. The descriptors are duplicated by the kernel, so
	// the caller may close them once WriteMsg returns.
	WriteMsg(p []byte, files Files) error

	// ReadMsg reads up to len(p) bytes and returns the file descriptors
	// received with them, if any. If the underlying transport was not
	// configured to accept file descriptors, no descriptors are returned.
	ReadMsg(p []byte) (n int, files Files, err error)
}

// malformedControlMessage marks a connection failure caused by unusable
// ancillary data. It is translated to ErrMalformedMessage, which closes
// the connection.
type malformedControlMessage struct{}

func (*malformedControlMessage) Error() string {
	return "varlink: truncated or malformed SCM_RIGHTS control message"
}

// closeFiles closes all non-nil files, ignoring errors. It is used to
// discard file descriptors that were received but not consumed.
func closeFiles(files Files) {
	files.Close()
}

// fdBox holds the file descriptors received with one call. Descriptors
// not taken are closed when the call finishes, so that misbehaving peers
// cannot exhaust the descriptor budget of the process. Ownership
// transfers on the first take: later takes return nil, so a handler that
// takes the descriptors twice cannot close the same files twice.
type fdBox struct {
	files Files
}

// take returns the descriptors held by the box and transfers their
// ownership to the caller, emptying the box. A second call returns nil.
func (b *fdBox) take() Files {
	if b == nil {
		return nil
	}
	files := b.files
	b.files = nil
	return files
}

// close closes the descriptors still held by the box, if any. Once take
// has been called, there is nothing left to close.
func (b *fdBox) close() {
	if b == nil {
		return
	}
	closeFiles(b.files)
}
