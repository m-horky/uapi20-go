package varlink

import (
	"context"
	"io"
	"time"
)

// ClientConfig contains transport policy for a client instance.
type ClientConfig struct {
	MaxFrameSize int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// AcceptFileDescriptors enables receiving file descriptors that the
	// service passes in SCM_RIGHTS ancillary messages with its replies.
	// Clients must not accept file descriptors unless they invoke methods
	// that return them, so it is off by default; descriptors received by
	// a client that did not opt in are discarded by the kernel. Sending
	// file descriptors (see SendFiles) does not require this option.
	AcceptFileDescriptors bool
}

// Client is the high-level v2 client API.
//
// Every operation works for all traffic and has no file-descriptor-aware
// results. File descriptor passing is opt-in per call through CallOption:
// SendFiles sends descriptors with the message, and ReceiveFiles receives
// the descriptors of a reply. See Files for the ownership rules.
type Client interface {
	// Invoke calls method and waits for its single reply. SendFiles sends
	// file descriptors with the call; sending them requires an AF_UNIX
	// connection and fails with ErrFileDescriptorsUnsupported otherwise.
	// ReceiveFiles receives the descriptors of the reply; receiving them
	// requires AcceptFileDescriptors.
	Invoke(ctx context.Context, method string, in any, out any, opts ...CallOption) error
	// Oneway calls method without waiting for a reply. SendFiles sends
	// file descriptors with the call; ReceiveFiles is not applicable and
	// fails with ErrInvalidCallOption.
	Oneway(ctx context.Context, method string, in any, opts ...CallOption) error
	// Stream calls a streaming method, sending files with the opening
	// call (SendFiles), and returns a receiver for its replies.
	Stream(ctx context.Context, method string, in any, opts ...CallOption) (Stream, error)
	// Upgrade calls method with the upgrade flag set. On success the
	// connection, including any bytes already received after that reply,
	// is returned for use by the upgraded protocol. SendFiles sends files
	// with the call; ReceiveFiles receives the descriptors of the
	// confirming reply.
	Upgrade(ctx context.Context, method string, in any, out any, opts ...CallOption) (UpgradedConn, error)
	// SupportsFileDescriptors reports whether the connection can pass
	// file descriptors.
	SupportsFileDescriptors() bool
	Batch() Batch
	io.Closer
}

// Batch pipelines multiple unary and oneway requests over one connection.
// Each queued call carries its options per message, like the unbatched
// API: SendFiles sends the files with the call, and the files received
// with an Invoke reply are stored by its ReceiveFiles option once Send has
// succeeded. Oneway calls produce no reply and accept no ReceiveFiles.
type Batch interface {
	Invoke(method string, in any, out any, opts ...CallOption)
	Oneway(method string, in any, opts ...CallOption)
	Send(ctx context.Context) error
}

// Stream receives zero or more replies from a streaming method.
type Stream interface {
	// Recv receives one reply. ReceiveFiles stores the file descriptors it
	// carries; receiving them requires ClientConfig.AcceptFileDescriptors.
	// SendFiles is not applicable and fails with ErrInvalidCallOption.
	Recv(ctx context.Context, out any, opts ...CallOption) error
	Close() error
}

// UpgradedConn is returned after a successful protocol upgrade.
type UpgradedConn interface {
	io.ReadWriteCloser
}
