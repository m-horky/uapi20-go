package varlink

import (
	"context"
	"errors"
	"io"
	"os"
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

// Invoke calls method and waits for its single reply.
func (c *ConnClient) Invoke(ctx context.Context, method string, in any, out any) error {
	_, err := c.invoke(ctx, method, in, out, nil)
	return err
}

// InvokeFDs calls method, sending files with the call and returning the
// files received with the reply.
func (c *ConnClient) InvokeFDs(ctx context.Context, method string, in any, out any, files []*os.File) ([]*os.File, error) {
	return c.invoke(ctx, method, in, out, files)
}

func (c *ConnClient) invoke(ctx context.Context, method string, in any, out any, files []*os.File) ([]*os.File, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	transport, err := c.ensureUsable()
	if err != nil {
		return nil, err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, false, false, false, files); err != nil {
		return nil, c.fail(err)
	}

	msg, replyFDs, err := readReplyFDs(ctx, transport, c.cfg.ReadTimeout, method)
	if err != nil {
		closeFiles(replyFDs)
		if isRemoteError(err) {
			return nil, err
		}
		return nil, c.fail(err)
	}
	if msg.Continues {
		closeFiles(replyFDs)
		return nil, c.fail(&ReplyError{Method: method, Err: ErrUnexpectedReply})
	}
	if msg.Parameters != nil {
		if err := codec.DecodeParameters(msg.Parameters, out); err != nil {
			closeFiles(replyFDs)
			return nil, &ReplyError{Method: method, Err: err}
		}
	}
	return replyFDs, nil
}

// Oneway calls method without waiting for (or allowing) a reply.
func (c *ConnClient) Oneway(ctx context.Context, method string, in any) error {
	return c.OnewayFDs(ctx, method, in, nil)
}

// OnewayFDs calls method without waiting for a reply, sending files with
// the call.
func (c *ConnClient) OnewayFDs(ctx context.Context, method string, in any, files []*os.File) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	transport, err := c.ensureUsable()
	if err != nil {
		return err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, false, true, false, files); err != nil {
		return c.fail(err)
	}
	return nil
}

// Stream calls method with the more flag set and returns a receiver for its
// replies.
func (c *ConnClient) Stream(ctx context.Context, method string, in any) (Stream, error) {
	c.opMu.Lock()
	transport, err := c.ensureUsable()
	if err != nil {
		c.opMu.Unlock()
		return nil, err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, true, false, false, nil); err != nil {
		c.opMu.Unlock()
		return nil, c.fail(err)
	}
	return &clientStream{
		client:    c,
		transport: transport,
		method:    method,
	}, nil
}

// Upgrade calls method with the upgrade flag set. On success the connection,
// including any bytes already received after the confirming reply, is
// returned for use by the upgraded protocol.
func (c *ConnClient) Upgrade(ctx context.Context, method string, in any, out any) (UpgradedConn, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	transport, err := c.ensureUsable()
	if err != nil {
		return nil, err
	}
	if err := sendRequest(ctx, transport, c.cfg.WriteTimeout, method, in, false, false, true, nil); err != nil {
		return nil, c.fail(err)
	}

	msg, replyFDs, err := readReplyFDs(ctx, transport, c.cfg.ReadTimeout, method)
	closeFiles(replyFDs)
	if err != nil {
		if isRemoteError(err) {
			return nil, err
		}
		return nil, c.fail(err)
	}
	if msg.Continues {
		return nil, c.fail(&ReplyError{Method: method, Err: ErrUnexpectedReply})
	}
	if msg.Parameters != nil {
		if err := codec.DecodeParameters(msg.Parameters, out); err != nil {
			return nil, c.fail(&ReplyError{Method: method, Err: err})
		}
	}

	conn := c.detach()
	if conn == nil {
		return nil, io.ErrClosedPipe
	}
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
func sendRequest(ctx context.Context, transport *framedTransport, timeout time.Duration, method string, in any, more, oneway, upgrade bool, files []*os.File) error {
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
func readReplyFDs(ctx context.Context, transport *framedTransport, timeout time.Duration, method string) (Message, []*os.File, error) {
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

// readReply reads and validates one reply, discarding any file descriptors
// received with it.
func readReply(ctx context.Context, transport *framedTransport, timeout time.Duration, method string) (Message, error) {
	msg, files, err := readReplyFDs(ctx, transport, timeout, method)
	closeFiles(files)
	return msg, err
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

func (s *clientStream) Recv(ctx context.Context, out any) error {
	_, err := s.recv(ctx, out)
	return err
}

// RecvFDs receives one reply and returns the file descriptors it carries.
func (s *clientStream) RecvFDs(ctx context.Context, out any) ([]*os.File, error) {
	return s.recv(ctx, out)
}

func (s *clientStream) recv(ctx context.Context, out any) ([]*os.File, error) {
	if s.closed {
		return nil, ErrStreamClosed
	}
	if s.done {
		return nil, io.EOF
	}

	msg, files, err := readReplyFDs(ctx, s.transport, s.client.cfg.ReadTimeout, s.method)
	if err != nil {
		closeFiles(files)
		client := s.client
		s.done = true
		s.release()
		if isRemoteError(err) || client == nil {
			return nil, err
		}
		return nil, client.fail(err)
	}
	if msg.Parameters != nil {
		if err := codec.DecodeParameters(msg.Parameters, out); err != nil {
			closeFiles(files)
			client := s.client
			s.done = true
			s.release()
			if client == nil {
				return nil, &ReplyError{Method: s.method, Err: err}
			}
			return nil, client.fail(&ReplyError{Method: s.method, Err: err})
		}
	}
	if !msg.Continues {
		s.done = true
		s.release()
	}
	return files, nil
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

type batchOp struct {
	method string
	in     any
	out    any
	oneway bool
}

type batchRequest struct {
	method string
	out    any
	oneway bool
	msg    Message
}

type clientBatch struct {
	client *ConnClient
	ops    []batchOp
	sent   bool
}

func (b *clientBatch) Invoke(method string, in any, out any) {
	b.ops = append(b.ops, batchOp{
		method: method,
		in:     in,
		out:    out,
	})
}

func (b *clientBatch) Oneway(method string, in any) {
	b.ops = append(b.ops, batchOp{
		method: method,
		in:     in,
		oneway: true,
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
	requests, err := b.prepareRequests()
	if err != nil {
		return err
	}

	writeErr := make(chan error, 1)
	go func() {
		for _, request := range requests {
			if err := transport.writeMessage(ctx, b.client.cfg.WriteTimeout, request.msg); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()
	writeDone := false
	for _, request := range requests {
		if request.oneway {
			continue
		}
		if !writeDone {
			select {
			case err := <-writeErr:
				if err != nil {
					return b.client.fail(err)
				}
				writeDone = true
			default:
			}
		}
		msg, files, err := readReplyFDs(ctx, transport, b.client.cfg.ReadTimeout, request.method)
		closeFiles(files)
		if err != nil {
			return b.client.fail(err)
		}
		if msg.Continues {
			return b.client.fail(&ReplyError{Method: request.method, Err: ErrUnexpectedReply})
		}
		if msg.Parameters != nil {
			if err := codec.DecodeParameters(msg.Parameters, request.out); err != nil {
				return b.client.fail(&ReplyError{Method: request.method, Err: err})
			}
		}
	}
	if !writeDone {
		if err := <-writeErr; err != nil {
			return b.client.fail(err)
		}
	}
	return nil
}

func (b *clientBatch) prepareRequests() ([]batchRequest, error) {
	requests := make([]batchRequest, len(b.ops))
	framer := codec.New(b.client.cfg.MaxFrameSize)
	for i, op := range b.ops {
		if op.method == "" {
			return nil, ErrInvalidMethod
		}
		params, err := codec.MarshalParameters(op.in)
		if err != nil {
			return nil, err
		}
		msg := Message{
			Method:     op.method,
			Parameters: params,
			Oneway:     op.oneway,
		}
		frame, err := codec.MarshalMessage(codec.Message(msg))
		if err != nil {
			return nil, err
		}
		if err := framer.WriteFrame(io.Discard, frame); err != nil {
			return nil, err
		}
		requests[i] = batchRequest{
			method: op.method,
			out:    op.out,
			oneway: op.oneway,
			msg:    msg,
		}
	}
	return requests, nil
}
