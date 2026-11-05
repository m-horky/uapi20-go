package varlink

import (
	"context"
	"io"
	"os"
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
	// file descriptors (see InvokeFDs and OnewayFDs) does not require
	// this option.
	AcceptFileDescriptors bool
}

// Client is the high-level v2 client API.
type Client interface {
	Invoke(ctx context.Context, method string, in any, out any) error
	Oneway(ctx context.Context, method string, in any) error
	Stream(ctx context.Context, method string, in any) (Stream, error)
	Upgrade(ctx context.Context, method string, in any, out any) (UpgradedConn, error)
	// InvokeFDs calls method, sending files with the call in SCM_RIGHTS
	// ancillary messages and returning the files received with the reply.
	// Receiving requires ClientConfig.AcceptFileDescriptors; sending
	// requires an AF_UNIX connection and fails with
	// ErrFileDescriptorsUnsupported otherwise.
	InvokeFDs(ctx context.Context, method string, in any, out any, files []*os.File) ([]*os.File, error)
	// OnewayFDs calls method without waiting for a reply, sending files
	// with the call.
	OnewayFDs(ctx context.Context, method string, in any, files []*os.File) error
	// SupportsFileDescriptors reports whether the connection can pass
	// file descriptors.
	SupportsFileDescriptors() bool
	Batch() Batch
	io.Closer
}

// Batch pipelines multiple unary and oneway requests over one connection.
// File descriptors cannot be passed in batched calls; replies carrying
// them have their descriptors discarded.
type Batch interface {
	Invoke(method string, in any, out any)
	Oneway(method string, in any)
	Send(ctx context.Context) error
}

// Stream receives zero or more replies from a streaming method.
type Stream interface {
	Recv(ctx context.Context, out any) error
	// RecvFDs receives one reply and returns the file descriptors it
	// carries. Receiving requires ClientConfig.AcceptFileDescriptors.
	RecvFDs(ctx context.Context, out any) ([]*os.File, error)
	Close() error
}

// UpgradedConn is returned after a successful protocol upgrade.
type UpgradedConn interface {
	io.ReadWriteCloser
}
