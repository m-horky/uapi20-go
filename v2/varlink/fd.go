package varlink

import (
	"errors"
	"io"
	"os"
)

// ErrFileDescriptorsUnsupported is returned when file descriptor passing
// is used on a transport that cannot carry file descriptors. Varlink is
// encapsulatable in transports other than AF_UNIX stream sockets, and file
// descriptors cannot be forwarded across such encapsulations; interfaces
// making use of file descriptor passing should document how their methods
// fail over such transports.
var ErrFileDescriptorsUnsupported = errors.New("varlink: file descriptor passing not supported by transport")

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
	WriteMsg(p []byte, files []*os.File) error

	// ReadMsg reads up to len(p) bytes and returns the file descriptors
	// received with them, if any. If the underlying transport was not
	// configured to accept file descriptors, no descriptors are returned.
	ReadMsg(p []byte) (n int, files []*os.File, err error)
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
func closeFiles(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// fdBox holds the file descriptors received with one call, and tracks
// whether the handler took ownership of them. Descriptors not taken are
// closed when the call finishes, so that misbehaving peers cannot exhaust
// the descriptor budget of the process.
type fdBox struct {
	files []*os.File
	taken bool
}

func (b *fdBox) take() []*os.File {
	if b == nil {
		return nil
	}
	b.taken = true
	return b.files
}

func (b *fdBox) close() {
	if b == nil || b.taken {
		return
	}
	closeFiles(b.files)
}
