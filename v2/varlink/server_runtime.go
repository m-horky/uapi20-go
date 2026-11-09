package varlink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/m-horky/uapi20-go/v2/internal/codec"
	"github.com/m-horky/uapi20-go/v2/varlink/idl"
)

// connQueueSize bounds the number of pipelined calls buffered per
// connection.
const connQueueSize = 16

// Serve serves requests on conn until the client disconnects, the context
// is canceled, or a protocol upgrade is confirmed.
//
// Calls are processed strictly in the order they are received. Handlers run
// with a context derived from ctx that is canceled when the client
// disconnects, so long-running operations abort when the connection goes
// away. On a confirmed upgrade the connection, including any bytes received
// after the upgrade call, is handed to the upgrade handler and Serve returns
// without closing it.
func (s *Server) Serve(ctx context.Context, conn io.ReadWriteCloser) error {
	transport := newFramedTransport(conn, s.cfg.MaxFrameSize, s.cfg.AcceptFileDescriptors)

	connCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// Peer credentials are properties of the connection, captured once
	// when it is accepted.
	if peer, ok := peerOf(conn); ok {
		connCtx = withPeer(connCtx, peer)
		if peer.PIDFD != nil {
			defer peer.PIDFD.Close()
		}
	}
	tagServerConn(conn)

	reader := newConnReader(transport, connCtx, cancel, s.cfg.ReadTimeout)
	go reader.run()

	owned := true
	defer func() {
		if owned {
			_ = conn.Close()
		}
	}()

	var serveErr error
	upgraded := false
	for {
		received, ok := <-reader.msgs
		if !ok {
			break
		}
		up, err := s.handleMessage(connCtx, transport, received.msg, received.fds)
		if err != nil {
			// Disconnects (including handlers aborted by a client
			// disconnect) end the connection cleanly; anything else is a
			// server error.
			if !isDisconnect(err) && !errors.Is(err, context.Canceled) {
				serveErr = err
			}
			break
		}
		if up {
			upgraded = true
			break
		}
		// The reader stops after delivering a call that requests an
		// upgrade, since the bytes following it may belong to the upgraded
		// protocol. A rejected upgrade restarts it.
		if received.msg.Upgrade {
			reader = newConnReader(transport, connCtx, cancel, s.cfg.ReadTimeout)
			go reader.run()
		}
	}

	cancel(nil)
	if upgraded {
		// The reader has already stopped after delivering the accepted
		// upgrade call, and the connection now belongs to the upgrade
		// handler.
		owned = false
		return nil
	}
	if serveErr == nil {
		switch cause := context.Cause(connCtx); cause {
		case nil, context.Canceled:
			// Clean disconnect or dispatcher-initiated teardown.
		default:
			serveErr = cause
		}
	}
	if serveErr == nil && ctx.Err() != nil {
		serveErr = ctx.Err()
	}

	if _, deadlines := conn.(deadlineReader); deadlines {
		// The context cancellation unblocked a possibly blocked read; wait
		// for the reader so it does not outlive this call. Connections
		// without deadline support are unblocked by the deferred Close.
		<-reader.done
	}

	if serveErr != nil {
		serveErr = s.reportError(ctx, serveErr)
	}
	return serveErr
}

// handleMessage dispatches one call, together with the file descriptors
// received with it. It reports whether the connection was upgraded, in
// which case Serve must stop reading and the caller owns the connection
// from now on.
func (s *Server) handleMessage(connCtx context.Context, transport *framedTransport, msg Message, files Files) (upgraded bool, err error) {
	// Descriptors the handler does not take ownership of are closed when
	// the call finishes, so that peers cannot exhaust the process
	// descriptor budget.
	fds := &fdBox{files: files}
	defer fds.close()

	if msg.Error != "" || msg.Continues {
		return false, fmt.Errorf("%w: client sent a reply message", ErrProtocolViolation)
	}
	if msg.Method == "" {
		return false, fmt.Errorf("%w: missing method", ErrProtocolViolation)
	}

	ifaceName, methodName, err := splitMethod(msg.Method)
	if err != nil {
		return false, s.replyServiceError(connCtx, transport, msg, ErrorInvalidParameter, "parameter", "method")
	}

	// Per-call authorization runs before any dispatch, so that denied
	// clients learn nothing about the interfaces the service implements.
	if s.cfg.Authorize != nil {
		if err := s.cfg.Authorize(connCtx, msg.Method); err != nil {
			return false, s.replyAuthorizationError(connCtx, transport, msg, err)
		}
	}

	iface, ok := s.ifaces[ifaceName]
	if !ok {
		return false, s.replyServiceError(connCtx, transport, msg, ErrorInterfaceNotFound, "interface", ifaceName)
	}

	var declared bool
	if iface.idl != nil {
		_, declared = iface.idl.Method(methodName)
	}
	if !declared {
		return false, s.replyServiceError(connCtx, transport, msg, ErrorMethodNotFound, "method", msg.Method)
	}

	unary, hasUnary := iface.unary[methodName]
	stream, hasStream := iface.stream[methodName]
	upgrade, hasUpgrade := iface.upgrade[methodName]
	if !hasUnary && !hasStream && !hasUpgrade {
		return false, s.replyServiceError(connCtx, transport, msg, ErrorMethodNotImplemented, "method", msg.Method)
	}

	switch {
	case hasStream && !msg.More:
		return false, s.replyServiceError(connCtx, transport, msg, ErrorExpectedMore, "method", msg.Method)
	case hasUpgrade && !msg.Upgrade:
		return false, s.replyServiceError(connCtx, transport, msg, ErrorExpectedUpgrade, "method", msg.Method)
	case hasUnary && msg.Upgrade:
		// The method does not take over the connection.
		return false, s.replyServiceError(connCtx, transport, msg, ErrorInvalidParameter, "parameter", "upgrade")
	}

	if iface.idl != nil {
		if err := iface.idl.ValidateCall(methodName, messageParameters(msg)); err != nil {
			var verr *idl.ValidationError
			if errors.As(err, &verr) {
				return false, s.replyServiceError(connCtx, transport, msg, ErrorInvalidParameter, "parameter", verr.Field)
			}
			return false, err
		}
	}

	callCtx, cancelCall := context.WithCancel(connCtx)
	defer cancelCall()

	call := &serverCall{
		method:     msg.Method,
		parameters: msg.Parameters,
		transport:  transport,
		cfg:        s.cfg,
		oneway:     msg.Oneway,
		fds:        fds,
	}

	switch {
	case hasUnary:
		// A call with more set to a method that does not support streaming
		// gets a single reply without continues.
		err = s.invokeHandler(func() error { return unary(callCtx, call) })
		if err == nil && !call.oneway && !call.replied {
			err = call.Reply(callCtx, nil)
		}
		return false, err

	case hasStream:
		err = s.invokeHandler(func() error { return stream(callCtx, call) })
		if err == nil && !call.replied && !call.closed {
			err = call.Close(callCtx, nil)
		}
		return false, err

	default:
		err = s.invokeHandler(func() error { return upgrade(callCtx, call) })
		if err == nil && !call.replied && !call.upgraded {
			_, err = call.Accept(callCtx, nil)
			return err == nil, err
		}
		return call.upgraded, err
	}
}

// invokeHandler runs fn, converting a panic into an error so that a broken
// handler closes the connection instead of taking down the server. The
// panic and its stack trace are logged first, so that handler bugs are
// diagnosable rather than silently swallowed.
func (s *Server) invokeHandler(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if s.cfg.Logger != nil {
				s.cfg.Logger.Printf("varlink: handler panic: %v\n%s", recovered, debug.Stack())
			}
			err = fmt.Errorf("varlink: handler panic: %v", recovered)
		}
	}()
	return fn()
}

func (s *Server) handleGetInfo(ctx context.Context, call UnaryCall) error {
	return call.Reply(ctx, struct {
		Vendor     string   `json:"vendor"`
		Product    string   `json:"product"`
		Version    string   `json:"version"`
		URL        string   `json:"url"`
		Interfaces []string `json:"interfaces"`
	}{
		Vendor:     s.info.Vendor,
		Product:    s.info.Product,
		Version:    s.info.Version,
		URL:        s.info.URL,
		Interfaces: s.interfaceNames(),
	})
}

func (s *Server) handleGetInterfaceDescription(ctx context.Context, call UnaryCall) error {
	var in struct {
		Interface string `json:"interface"`
	}
	if err := call.Decode(&in); err != nil || in.Interface == "" {
		return call.ReplyError(ctx, ErrorInvalidParameter, map[string]string{"parameter": "interface"})
	}
	iface, ok := s.ifaces[in.Interface]
	if !ok {
		return call.ReplyError(ctx, ErrorInterfaceNotFound, map[string]string{"interface": in.Interface})
	}
	return call.Reply(ctx, struct {
		Description string `json:"description"`
	}{Description: iface.description})
}

func (s *Server) interfaceNames() []string {
	names := make([]string, 0, len(s.order))
	names = append(names, ServiceInterfaceName)
	for _, name := range s.order {
		if name != ServiceInterfaceName {
			names = append(names, name)
		}
	}
	return names
}

// replyServiceError writes a generic org.varlink.service error reply.
// Replies to oneway calls are suppressed.
func (s *Server) replyServiceError(ctx context.Context, transport *framedTransport, request Message, name, key, value string) error {
	if request.Oneway {
		return nil
	}
	params, err := codec.MarshalParameters(map[string]string{key: value})
	if err != nil {
		return err
	}
	return transport.writeMessage(ctx, s.cfg.WriteTimeout, Message{
		Error:      name,
		Parameters: params,
	})
}

// replyAuthorizationError maps a denial from the Authorize hook to a wire
// reply. A *RemoteError returned by the hook is passed through unchanged;
// any other error is reported as org.varlink.service.PermissionDenied.
// Oneway calls are never replied to.
func (s *Server) replyAuthorizationError(ctx context.Context, transport *framedTransport, request Message, err error) error {
	if request.Oneway {
		return nil
	}
	var remote *RemoteError
	if errors.As(err, &remote) {
		var params *codec.RawValue
		if remote.Parameters != nil {
			p := codec.RawValue(remote.Parameters)
			params = &p
		}
		return transport.writeMessage(ctx, s.cfg.WriteTimeout, Message{
			Error:      remote.Name,
			Parameters: params,
		})
	}
	return transport.writeMessage(ctx, s.cfg.WriteTimeout, Message{Error: ErrorPermissionDenied})
}

func (s *Server) reportError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if s.cfg.Logger != nil {
		s.cfg.Logger.Printf("varlink server error: %v", err)
	}
	if s.cfg.ErrorHandler != nil {
		s.cfg.ErrorHandler(ctx, err)
	}
	return err
}

func messageParameters(msg Message) []byte {
	if msg.Parameters == nil {
		return nil
	}
	return *msg.Parameters
}

func isDisconnect(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}

// connReader reads messages from the connection and delivers them, in
// order, to the dispatcher. Its lifecycle is tied to the per-connection
// context: it fails (and cancels the context) when the connection breaks,
// which also aborts any handler in flight. It stops after delivering a call
// that requests a protocol upgrade, since the bytes following such a call
// may belong to the upgraded protocol and must not be consumed before the
// upgrade is confirmed; Serve restarts it if the upgrade is rejected.
type connReader struct {
	transport *framedTransport
	ctx       context.Context
	cancel    context.CancelCauseFunc
	timeout   time.Duration

	msgs chan receivedMessage
	done chan struct{}
}

// receivedMessage is one decoded call together with the file descriptors
// received with it.
type receivedMessage struct {
	msg Message
	fds Files
}

func newConnReader(transport *framedTransport, ctx context.Context, cancel context.CancelCauseFunc, timeout time.Duration) *connReader {
	return &connReader{
		transport: transport,
		ctx:       ctx,
		cancel:    cancel,
		timeout:   timeout,
		msgs:      make(chan receivedMessage, connQueueSize),
		done:      make(chan struct{}),
	}
}

func (r *connReader) run() {
	defer close(r.msgs)
	defer close(r.done)
	for {
		msg, files, err := r.transport.readMessageFDs(r.ctx, r.timeout)
		if err != nil {
			closeFiles(files)
			r.fail(err)
			return
		}
		select {
		case r.msgs <- receivedMessage{msg: msg, fds: files}:
		case <-r.ctx.Done():
			closeFiles(files)
			return
		}
		if msg.Upgrade {
			// The dispatcher decides the fate of the connection; do not
			// read past the upgrade call.
			return
		}
	}
}

// fail records err as the reason for the connection ending and cancels the
// per-connection context, aborting any call in flight.
func (r *connReader) fail(err error) {
	if isDisconnect(err) {
		err = nil
	}
	r.cancel(err)
}

// serverCall is the server-side API for one request. It implements
// UnaryCall, StreamCall, and UpgradeCall; which subset a handler may use is
// determined by how the method was registered.
//
// A call is not safe for concurrent use: its methods must be called from
// the goroutine running the handler, since replies are matched to calls in
// dispatch order.
type serverCall struct {
	method     string
	parameters *codec.RawValue
	transport  *framedTransport
	cfg        ServerConfig
	oneway     bool
	fds        *fdBox

	// pushed holds the descriptors queued by PushFile and its siblings
	// for the next reply written for this call.
	pushed Files

	replied  bool
	closed   bool
	upgraded bool
}

var (
	_ UnaryCall   = (*serverCall)(nil)
	_ StreamCall  = (*serverCall)(nil)
	_ UpgradeCall = (*serverCall)(nil)
)

func (c *serverCall) Method() string {
	return c.method
}

func (c *serverCall) Decode(out any) error {
	return codec.DecodeParameters(c.parameters, out)
}

// Files returns the file descriptors received with the call.
func (c *serverCall) Files() Files {
	return c.fds.take()
}

// PushFile queues f for the next reply of this call and returns its index.
func (c *serverCall) PushFile(f *os.File) (int, error) {
	if f == nil {
		return -1, ErrInvalidCallOption
	}
	if c.transport.fd == nil {
		return -1, ErrFileDescriptorsUnsupported
	}
	if len(c.pushed) >= maxSCMFileDescriptors {
		return -1, ErrFileDescriptorsUnsupported
	}
	c.pushed = append(c.pushed, f)
	return len(c.pushed) - 1, nil
}

// PushFiles queues all files for the next reply of this call and returns
// their indexes.
func (c *serverCall) PushFiles(files Files) ([]int, error) {
	indexes := make([]int, 0, len(files))
	for _, f := range files {
		i, err := c.PushFile(f)
		if err != nil {
			return nil, err
		}
		indexes = append(indexes, i)
	}
	return indexes, nil
}

// takePushed returns the descriptors queued for the next reply and empties
// the queue, so that each queued descriptor is sent at most once.
func (c *serverCall) takePushed() Files {
	files := c.pushed
	c.pushed = nil
	return files
}

// SupportsFileDescriptors reports whether the connection can pass file
// descriptors.
func (c *serverCall) SupportsFileDescriptors() bool {
	return c.transport.fd != nil
}

func (c *serverCall) IsOneway() bool {
	return c.oneway
}

// write marshals out and writes it as one reply, sending the queued
// descriptors with it and marking it as continued when it is an
// intermediate reply of a streaming call.
func (c *serverCall) write(ctx context.Context, out any, continues bool) error {
	params, err := codec.MarshalParameters(out)
	if err != nil {
		return err
	}
	return c.transport.writeMessageFDs(ctx, c.cfg.WriteTimeout, Message{
		Parameters: params,
		Continues:  continues,
	}, c.takePushed())
}

// Reply replies with out, sending the queued descriptors with it.
func (c *serverCall) Reply(ctx context.Context, out any) error {
	if c.replied || c.upgraded {
		return ErrProtocolViolation
	}
	if c.oneway {
		c.replied = true
		c.pushed = nil
		return nil
	}
	if err := c.write(ctx, out, false); err != nil {
		return err
	}
	c.replied = true
	return nil
}

// ReplyError replies with a varlink error, sending the queued descriptors
// with it. Replies to oneway calls are suppressed.
func (c *serverCall) ReplyError(ctx context.Context, name string, parameters any) error {
	if c.replied || c.closed || c.upgraded {
		return ErrProtocolViolation
	}
	if c.oneway {
		c.replied = true
		c.pushed = nil
		return nil
	}
	params, err := codec.MarshalParameters(parameters)
	if err != nil {
		return err
	}
	c.replied = true
	return c.transport.writeMessageFDs(ctx, c.cfg.WriteTimeout, Message{
		Error:      name,
		Parameters: params,
	}, c.takePushed())
}

// Send sends one streaming reply, sending the queued descriptors with it.
func (c *serverCall) Send(ctx context.Context, out any) error {
	if c.replied || c.closed || c.upgraded {
		return ErrProtocolViolation
	}
	return c.write(ctx, out, true)
}

// Close sends the final reply, sending the queued descriptors with it.
func (c *serverCall) Close(ctx context.Context, out any) error {
	if c.replied || c.closed || c.upgraded {
		return ErrProtocolViolation
	}
	if err := c.write(ctx, out, false); err != nil {
		return err
	}
	c.replied = true
	c.closed = true
	return nil
}

// Accept confirms the upgrade, replying with out and sending the queued
// descriptors with it, and returns the connection, including any bytes
// already received after the upgrade call, for use by the upgraded
// protocol.
func (c *serverCall) Accept(ctx context.Context, out any) (io.ReadWriteCloser, error) {
	if c.replied || c.upgraded {
		return nil, ErrProtocolViolation
	}
	params, err := codec.MarshalParameters(out)
	if err != nil {
		return nil, err
	}
	c.replied = true
	c.upgraded = true
	if err := c.transport.writeMessageFDs(ctx, c.cfg.WriteTimeout, Message{
		Parameters: params,
	}, c.takePushed()); err != nil {
		return nil, err
	}
	// The reader has already stopped after delivering the upgrade call, so
	// it cannot consume bytes of the upgraded protocol.
	return c.transport.detach(), nil
}
