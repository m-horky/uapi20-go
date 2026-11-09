package varlink

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/m-horky/uapi20-go/v2/internal/codec"
)

// ConnClient is a framed varlink client over an existing transport connection.
type ConnClient struct {
	cfg       ClientConfig
	transport *framedTransport

	opMu    sync.Mutex
	stateMu sync.Mutex
	closed  bool
}

var _ Client = (*ConnClient)(nil)
var _ Batch = (*clientBatch)(nil)

// NewClient returns a v2 client over conn.
func NewClient(conn io.ReadWriteCloser, cfg ClientConfig) *ConnClient {
	if cfg.MaxFrameSize == 0 {
		cfg.MaxFrameSize = MaxFrameSizeDefault
	}
	return &ConnClient{
		cfg:       cfg,
		transport: newFramedTransport(conn, cfg.MaxFrameSize, cfg.AcceptFileDescriptors),
	}
}

// Invoke calls method and waits for its single reply. See Client.Invoke
// for the file descriptor options.
func (c *ConnClient) Invoke(ctx context.Context, method string, in any, out any, opts ...CallOption) error {
	return c.invoke(ctx, method, in, out, newCallOptions(opts))
}

// invoke performs one unary call with the effective call options.
func (c *ConnClient) invoke(ctx context.Context, method string, in any, out any, opts callOptions) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	transport, err := c.ensureUsable()
	if err != nil {
		return err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, false, false, false, opts.files); err != nil {
		return c.fail(err)
	}

	msg, replyFDs, err := readReplyFDs(ctx, transport, c.cfg.ReadTimeout, method)
	if err != nil {
		// Descriptors received with an error reply are not handed to the
		// caller: ReceiveFiles stores them only on success.
		replyFDs.Close()
		if isRemoteError(err) {
			return err
		}
		return c.fail(err)
	}
	if err := decodeFinalReply(method, msg, out); err != nil {
		replyFDs.Close()
		// A continues reply to a non-streaming call is a protocol
		// violation; a decode failure leaves the connection usable.
		if errors.Is(err, ErrUnexpectedReply) {
			return c.fail(err)
		}
		return err
	}
	opts.deliver(replyFDs)
	return nil
}

// Oneway calls method without waiting for (or allowing) a reply. See
// Client.Oneway for the file descriptor options.
func (c *ConnClient) Oneway(ctx context.Context, method string, in any, opts ...CallOption) error {
	o := newCallOptions(opts)
	if err := o.requireNoReply(); err != nil {
		return err
	}

	c.opMu.Lock()
	defer c.opMu.Unlock()

	transport, err := c.ensureUsable()
	if err != nil {
		return err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, false, true, false, o.files); err != nil {
		return c.fail(err)
	}
	return nil
}

// Stream calls method with the more flag set and returns a receiver for
// its replies. SendFiles sends files with the opening call; ReceiveFiles is
// not applicable to it.
func (c *ConnClient) Stream(ctx context.Context, method string, in any, opts ...CallOption) (Stream, error) {
	o := newCallOptions(opts)
	if err := o.requireNoReply(); err != nil {
		return nil, err
	}

	c.opMu.Lock()
	transport, err := c.ensureUsable()
	if err != nil {
		c.opMu.Unlock()
		return nil, err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, true, false, false, o.files); err != nil {
		c.opMu.Unlock()
		return nil, c.fail(err)
	}
	return &clientStream{
		client:    c,
		transport: transport,
		method:    method,
	}, nil
}

// Upgrade calls method with the upgrade flag set. On success the
// connection, including any bytes already received after that reply, is
// returned for use by the upgraded protocol. See Client.Upgrade for the
// file descriptor options.
func (c *ConnClient) Upgrade(ctx context.Context, method string, in any, out any, opts ...CallOption) (UpgradedConn, error) {
	o := newCallOptions(opts)

	c.opMu.Lock()
	defer c.opMu.Unlock()

	transport, err := c.ensureUsable()
	if err != nil {
		return nil, err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, false, false, true, o.files); err != nil {
		return nil, c.fail(err)
	}

	msg, replyFDs, err := readReplyFDs(ctx, transport, c.cfg.ReadTimeout, method)
	if err != nil {
		// Descriptors received with an error reply are not handed to the
		// caller: ReceiveFiles stores them only on success.
		replyFDs.Close()
		if isRemoteError(err) {
			return nil, err
		}
		return nil, c.fail(err)
	}
	if err := decodeFinalReply(method, msg, out); err != nil {
		replyFDs.Close()
		return nil, c.fail(err)
	}

	conn := c.detach()
	if conn == nil {
		replyFDs.Close()
		return nil, io.ErrClosedPipe
	}
	o.deliver(replyFDs)
	return conn, nil
}

// SupportsFileDescriptors reports whether the connection can pass file
// descriptors.
func (c *ConnClient) SupportsFileDescriptors() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.transport != nil && c.transport.fd != nil
}

// Batch returns a batch for pipelined unary and oneway calls.
func (c *ConnClient) Batch() Batch {
	return &clientBatch{client: c}
}

// Close closes the underlying connection. In-flight calls fail.
func (c *ConnClient) Close() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.transport == nil || c.transport.conn == nil {
		return nil
	}
	err := c.transport.conn.Close()
	c.transport = nil
	return err
}

// ensureUsable returns the transport to use for a new operation. The
// snapshot stays valid for the operation even if Close runs concurrently.
func (c *ConnClient) ensureUsable() (*framedTransport, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed || c.transport == nil || c.transport.conn == nil {
		return nil, io.ErrClosedPipe
	}
	return c.transport, nil
}

func (c *ConnClient) detach() io.ReadWriteCloser {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed || c.transport == nil {
		return nil
	}
	conn := c.transport.detach()
	c.closed = true
	c.transport = nil
	return conn
}

func (c *ConnClient) fail(err error) error {
	_ = c.Close()
	return err
}

// sendRequest writes one call message, attaching files to it when the
// transport supports file descriptor passing.
func sendRequest(ctx context.Context, transport *framedTransport, timeout time.Duration, method string, in any, more, oneway, upgrade bool, files Files) error {
	if method == "" {
		return ErrInvalidMethod
	}
	params, err := codec.MarshalParameters(in)
	if err != nil {
		return err
	}
	return transport.writeMessageFDs(ctx, timeout, Message{
		Method:     method,
		Parameters: params,
		More:       more,
		Oneway:     oneway,
		Upgrade:    upgrade,
	}, files)
}

func isRemoteError(err error) bool {
	var remote *RemoteError
	return errors.As(err, &remote)
}

// readReplyFDs reads and validates one reply to a call on method, returning
// the file descriptors received with it.
func readReplyFDs(ctx context.Context, transport *framedTransport, timeout time.Duration, method string) (Message, Files, error) {
	msg, files, err := transport.readMessageFDs(ctx, timeout)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Message{}, nil, err
	}
	if msg.Method != "" || msg.More || msg.Oneway || msg.Upgrade {
		closeFiles(files)
		return Message{}, nil, &ReplyError{Method: method, Err: ErrProtocolViolation}
	}
	if remote := remoteErrorFromMessage(msg); remote != nil {
		return Message{}, files, remote
	}
	return msg, files, nil
}

// decodeFinalReply validates that msg terminates the exchange for method
// and decodes its parameters into out. A continues reply is rejected as
// ErrUnexpectedReply.
func decodeFinalReply(method string, msg Message, out any) error {
	if msg.Continues {
		return &ReplyError{Method: method, Err: ErrUnexpectedReply}
	}
	if msg.Parameters != nil {
		if err := codec.DecodeParameters(msg.Parameters, out); err != nil {
			return &ReplyError{Method: method, Err: err}
		}
	}
	return nil
}

// GetInfo calls org.varlink.service.GetInfo and returns the service
// metadata and the names of the interfaces it implements.
func (c *ConnClient) GetInfo(ctx context.Context) (ServiceInfo, []string, error) {
	var out struct {
		Vendor     string   `json:"vendor"`
		Product    string   `json:"product"`
		Version    string   `json:"version"`
		URL        string   `json:"url"`
		Interfaces []string `json:"interfaces"`
	}
	if err := c.Invoke(ctx, "org.varlink.service.GetInfo", nil, &out); err != nil {
		return ServiceInfo{}, nil, err
	}
	return ServiceInfo{
		Vendor:  out.Vendor,
		Product: out.Product,
		Version: out.Version,
		URL:     out.URL,
	}, out.Interfaces, nil
}

// GetInterfaceDescription calls org.varlink.service.GetInterfaceDescription
// for iface and returns the interface description source.
func (c *ConnClient) GetInterfaceDescription(ctx context.Context, iface string) (string, error) {
	var out struct {
		Description string `json:"description"`
	}
	in := struct {
		Interface string `json:"interface"`
	}{Interface: iface}
	if err := c.Invoke(ctx, "org.varlink.service.GetInterfaceDescription", &in, &out); err != nil {
		return "", err
	}
	return out.Description, nil
}

type clientStream struct {
	client    *ConnClient
	transport *framedTransport
	method    string
	done      bool
	closed    bool
}

// Recv receives one reply. See Stream.Recv for the file descriptor
// options.
func (s *clientStream) Recv(ctx context.Context, out any, opts ...CallOption) error {
	o := newCallOptions(opts)
	if err := o.requireNoFiles(); err != nil {
		return err
	}

	if s.closed {
		return ErrStreamClosed
	}
	if s.done {
		return io.EOF
	}

	msg, files, err := readReplyFDs(ctx, s.transport, s.client.cfg.ReadTimeout, s.method)
	if err != nil {
		client := s.client
		s.done = true
		s.release()
		// Descriptors received with an error reply are not handed to the
		// caller: ReceiveFiles stores them only on success.
		files.Close()
		if isRemoteError(err) || client == nil {
			return err
		}
		return client.fail(err)
	}
	if msg.Parameters != nil {
		if err := codec.DecodeParameters(msg.Parameters, out); err != nil {
			closeFiles(files)
			client := s.client
			s.done = true
			s.release()
			if client == nil {
				return &ReplyError{Method: s.method, Err: err}
			}
			return client.fail(&ReplyError{Method: s.method, Err: err})
		}
	}
	if !msg.Continues {
		s.done = true
		s.release()
	}
	o.deliver(files)
	return nil
}

func (s *clientStream) Close() error {
	if s.done {
		return nil
	}
	s.done = true
	s.closed = true
	client := s.client
	s.release()
	if client == nil {
		return nil
	}
	return client.Close()
}

// release returns the client operation lock to the client.
func (s *clientStream) release() {
	if s.client != nil {
		s.client.opMu.Unlock()
		s.client = nil
	}
}

// batchOp is one call queued in a batch, together with its effective call
// options.
type batchOp struct {
	method string
	in     any
	out    any
	oneway bool
	opts   callOptions
}

type clientBatch struct {
	client *ConnClient
	ops    []batchOp
	sent   bool
}

// Invoke queues a unary call with the given options; SendFiles sends
// files with it, and ReceiveFiles stores the files of its reply once Send
// has succeeded.
func (b *clientBatch) Invoke(method string, in any, out any, opts ...CallOption) {
	b.ops = append(b.ops, batchOp{
		method: method,
		in:     in,
		out:    out,
		opts:   newCallOptions(opts),
	})
}

// Oneway queues a oneway call with the given options; SendFiles sends
// files with it. ReceiveFiles does not apply to oneway calls; passing it
// makes Send fail with ErrInvalidCallOption.
func (b *clientBatch) Oneway(method string, in any, opts ...CallOption) {
	b.ops = append(b.ops, batchOp{
		method: method,
		in:     in,
		oneway: true,
		opts:   newCallOptions(opts),
	})
}

func (b *clientBatch) Send(ctx context.Context) error {
	if b.sent {
		return ErrBatchSent
	}
	b.sent = true
	if b.client == nil {
		return io.ErrClosedPipe
	}
	if len(b.ops) == 0 {
		return nil
	}

	b.client.opMu.Lock()
	defer b.client.opMu.Unlock()

	transport, err := b.client.ensureUsable()
	if err != nil {
		return err
	}

	// Write all requests first, then read the replies in order. Frame size
	// limits are enforced when each message is written.
	for _, op := range b.ops {
		if err := sendRequest(ctx, transport, b.client.cfg.WriteTimeout, op.method, op.in, false, op.oneway, false, op.opts.files); err != nil {
			return b.client.fail(err)
		}
	}
	for _, op := range b.ops {
		if op.oneway {
			if op.opts.reply != nil {
				return b.client.fail(ErrInvalidCallOption)
			}
			continue
		}
		msg, files, err := readReplyFDs(ctx, transport, b.client.cfg.ReadTimeout, op.method)
		if err != nil {
			// The replies queued behind the failed one are unread, so the
			// connection is no longer usable; descriptors received with
			// the error reply are closed, since ReceiveFiles stores them
			// only on success.
			files.Close()
			return b.client.fail(err)
		}
		if err := decodeFinalReply(op.method, msg, op.out); err != nil {
			files.Close()
			return b.client.fail(err)
		}
		op.opts.deliver(files)
	}
	return nil
}
