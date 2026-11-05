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

	"github.com/varlink/go/v2/internal/codec"
	"github.com/varlink/go/v2/varlink/idl"
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
	for received := range reader.msgs {
		up, err := s.handleMessage(connCtx, transport, received.msg, received.fds, reader)
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
	}

	cancel(nil)
	if upgraded {
		// The reader has already been halted by the accepted upgrade, and
		// the connection now belongs to the upgrade handler.
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
func (s *Server) handleMessage(connCtx context.Context, transport *framedTransport, msg Message, files []*os.File, reader *connReader) (upgraded bool, err error) {
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

	// The reader parks after delivering an upgrade call, because the bytes
	// following it may already belong to the upgraded protocol. Make sure it
	// is resumed unless the upgrade is confirmed.
	if msg.Upgrade {
		defer func() {
			if !upgraded {
				reader.resume()
			}
		}()
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

	request := baseRequest{
		method:     msg.Method,
		parameters: msg.Parameters,
		transport:  transport,
		cfg:        s.cfg,
		oneway:     msg.Oneway,
		reader:     reader,
		fds:        fds,
	}

	switch {
	case hasUnary:
		// A call with more set to a method that does not support streaming
		// gets a single reply without continues.
		call := &unaryRequest{baseRequest: request}
		err = s.invokeHandler(func() error { return unary(callCtx, call) })
		if err == nil && !call.oneway && !call.replied {
			err = call.Reply(callCtx, nil)
		}
		return false, err

	case hasStream:
		call := &streamRequest{baseRequest: request}
		err = s.invokeHandler(func() error { return stream(callCtx, call) })
		if err == nil && !call.replied && !call.closed {
			err = call.Close(callCtx, nil)
		}
		return false, err

	default:
		call := &upgradeRequest{baseRequest: request}
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
// and it parks after delivering a call that requests a protocol upgrade,
// since the bytes following such a call may belong to the upgraded protocol
// and must not be consumed before the upgrade is confirmed.
type connReader struct {
	transport *framedTransport
	ctx       context.Context
	cancel    context.CancelCauseFunc
	timeout   time.Duration

	msgs    chan receivedMessage
	control chan readerControl
	done    chan struct{}
}

// receivedMessage is one decoded call together with the file descriptors
// received with it.
type receivedMessage struct {
	msg Message
	fds []*os.File
}

type readerControl struct {
	halt bool
}

func newConnReader(transport *framedTransport, ctx context.Context, cancel context.CancelCauseFunc, timeout time.Duration) *connReader {
	return &connReader{
		transport: transport,
		ctx:       ctx,
		cancel:    cancel,
		timeout:   timeout,
		msgs:      make(chan receivedMessage, connQueueSize),
		control:   make(chan readerControl),
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
			return
		}
		if !msg.Upgrade {
			continue
		}
		// Wait for the dispatcher's decision on the upgrade.
		select {
		case c := <-r.control:
			if c.halt {
				return
			}
		case <-r.ctx.Done():
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

// halt stops the reader and waits for it to exit. It is called when an
// upgrade is confirmed, with the reader parked.
func (r *connReader) halt() {
	select {
	case r.control <- readerControl{halt: true}:
	case <-r.done:
	}
	<-r.done
}

// resume lets the reader continue after an upgrade call was rejected.
func (r *connReader) resume() {
	select {
	case r.control <- readerControl{halt: false}:
	case <-r.done:
	}
}

type baseRequest struct {
	method     string
	parameters *codec.RawValue
	transport  *framedTransport
	cfg        ServerConfig
	oneway     bool
	replied    bool
	upgraded   bool
	reader     *connReader
	fds        *fdBox
}

func (r *baseRequest) Method() string {
	return r.method
}

func (r *baseRequest) Decode(out any) error {
	return codec.DecodeParameters(r.parameters, out)
}

// FileDescriptors returns the file descriptors received with the call.
func (r *baseRequest) FileDescriptors() []*os.File {
	return r.fds.take()
}

func (r *baseRequest) ReplyError(ctx context.Context, name string, parameters any) error {
	if r.replied || r.upgraded {
		return ErrProtocolViolation
	}
	if r.oneway {
		r.replied = true
		return nil
	}
	params, err := codec.MarshalParameters(parameters)
	if err != nil {
		return err
	}
	r.replied = true
	return r.transport.writeMessage(ctx, r.cfg.WriteTimeout, Message{
		Error:      name,
		Parameters: params,
	})
}

type unaryRequest struct {
	baseRequest
}

func (r *unaryRequest) Reply(ctx context.Context, out any) error {
	return r.ReplyFDs(ctx, out, nil)
}

// ReplyFDs replies with out, attaching files to the reply.
func (r *unaryRequest) ReplyFDs(ctx context.Context, out any, files []*os.File) error {
	if r.replied || r.upgraded {
		return ErrProtocolViolation
	}
	if r.oneway {
		r.replied = true
		return nil
	}
	params, err := codec.MarshalParameters(out)
	if err != nil {
		return err
	}
	r.replied = true
	return r.transport.writeMessageFDs(ctx, r.cfg.WriteTimeout, Message{
		Parameters: params,
	}, files)
}

func (r *unaryRequest) IsOneway() bool {
	return r.oneway
}

type streamRequest struct {
	baseRequest
	closed bool
}

func (r *streamRequest) Send(ctx context.Context, out any) error {
	return r.SendFDs(ctx, out, nil)
}

// SendFDs sends one streaming reply, attaching files to it.
func (r *streamRequest) SendFDs(ctx context.Context, out any, files []*os.File) error {
	if r.replied || r.closed || r.upgraded {
		return ErrProtocolViolation
	}
	params, err := codec.MarshalParameters(out)
	if err != nil {
		return err
	}
	return r.transport.writeMessageFDs(ctx, r.cfg.WriteTimeout, Message{
		Parameters: params,
		Continues:  true,
	}, files)
}

func (r *streamRequest) Close(ctx context.Context, out any) error {
	return r.CloseFDs(ctx, out, nil)
}

// CloseFDs sends the final reply, attaching files to it.
func (r *streamRequest) CloseFDs(ctx context.Context, out any, files []*os.File) error {
	if r.replied || r.closed || r.upgraded {
		return ErrProtocolViolation
	}
	params, err := codec.MarshalParameters(out)
	if err != nil {
		return err
	}
	r.replied = true
	r.closed = true
	return r.transport.writeMessageFDs(ctx, r.cfg.WriteTimeout, Message{
		Parameters: params,
	}, files)
}

type upgradeRequest struct {
	baseRequest
}

func (r *upgradeRequest) Accept(ctx context.Context, out any) (io.ReadWriteCloser, error) {
	if r.replied || r.upgraded {
		return nil, ErrProtocolViolation
	}
	params, err := codec.MarshalParameters(out)
	if err != nil {
		return nil, err
	}
	r.replied = true
	r.upgraded = true
	if err := r.transport.writeMessage(ctx, r.cfg.WriteTimeout, Message{
		Parameters: params,
	}); err != nil {
		return nil, err
	}
	// Stop the reader before handing over the connection: it must not
	// consume bytes of the upgraded protocol.
	r.reader.halt()
	return r.transport.detach(), nil
}
