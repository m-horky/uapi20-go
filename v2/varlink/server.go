package varlink

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/varlink/go/v2/varlink/idl"
)

// Logger matches the subset of loggers the server needs.
type Logger interface {
	Printf(format string, v ...any)
}

// ServerConfig contains runtime policy for a server.
type ServerConfig struct {
	// MaxFrameSize bounds the size of a single message. Connections that
	// exceed it are closed.
	MaxFrameSize int
	// ReadTimeout bounds the time to read a single message. Zero means no
	// timeout.
	ReadTimeout time.Duration
	// WriteTimeout bounds the time to write a single message. Zero means
	// no timeout.
	WriteTimeout time.Duration
	// Logger receives connection-level errors.
	Logger Logger
	// ErrorHandler receives connection-level errors after they are logged.
	ErrorHandler func(context.Context, error)
	// Authorize, if set, is called before every method call is dispatched.
	// The context carries the peer credentials of the connection (see
	// PeerFromContext). Returning a *RemoteError replies with that error;
	// returning any other error replies with
	// org.varlink.service.PermissionDenied. Returning nil dispatches the
	// call as usual.
	Authorize func(ctx context.Context, method string) error
	// AcceptFileDescriptors enables receiving file descriptors that
	// clients pass in SCM_RIGHTS ancillary messages with their calls.
	// Services must not accept file descriptors unless they implement an
	// interface that makes use of them, so it is off by default. On Linux
	// 6.16 and later, unix listeners turn off the SO_PASSRIGHTS socket
	// option in that case, so that a client sending descriptors has its
	// call fail rather than the descriptors being queued; on other
	// kernels the descriptors are discarded when the connection is read.
	// Sending file descriptors (see UnaryCall.ReplyFDs and
	// StreamCall.SendFDs) does not require this option.
	AcceptFileDescriptors bool
	// MaxConnections bounds the number of connections served
	// concurrently per listener; connections beyond the limit are closed
	// immediately. It bounds the resources (goroutines, buffers) a peer
	// can cause the server to allocate. Zero means unlimited.
	MaxConnections int
}

// ServiceInfo describes the service metadata exposed through org.varlink.service.
type ServiceInfo struct {
	Vendor  string
	Product string
	Version string
	URL     string
}

// UnaryHandler processes one request and returns one reply. Unary handlers
// also serve oneway calls, which have no reply; see UnaryCall.IsOneway.
type UnaryHandler func(ctx context.Context, call UnaryCall) error

// StreamHandler processes one request and emits zero or more replies.
type StreamHandler func(ctx context.Context, call StreamCall) error

// UpgradeHandler processes one request and hands ownership of the connection
// to the caller.
type UpgradeHandler func(ctx context.Context, call UpgradeCall) error

// UnaryCall is the server-side API for a non-streaming request.
//
// A call is not safe for concurrent use: its methods must be called from
// the goroutine running the handler, since replies are matched to calls in
// dispatch order.
type UnaryCall interface {
	Method() string
	Decode(out any) error
	Reply(ctx context.Context, out any) error
	// ReplyFDs replies with out, attaching files to the reply in
	// SCM_RIGHTS ancillary messages. It fails with
	// ErrFileDescriptorsUnsupported on transports that cannot carry file
	// descriptors.
	ReplyFDs(ctx context.Context, out any, files []*os.File) error
	ReplyError(ctx context.Context, name string, parameters any) error
	IsOneway() bool
	// FileDescriptors returns the file descriptors received with the
	// call, referenced from its parameters by their index. The first call
	// transfers ownership: descriptors the handler does not take are
	// closed when it returns. It returns nil unless the server was
	// configured with AcceptFileDescriptors.
	FileDescriptors() []*os.File
}

// StreamCall is the server-side API for a streaming request.
type StreamCall interface {
	Method() string
	Decode(out any) error
	Send(ctx context.Context, out any) error
	// SendFDs sends one streaming reply, attaching files to it in
	// SCM_RIGHTS ancillary messages.
	SendFDs(ctx context.Context, out any, files []*os.File) error
	Close(ctx context.Context, out any) error
	// CloseFDs sends the final reply, attaching files to it in SCM_RIGHTS
	// ancillary messages.
	CloseFDs(ctx context.Context, out any, files []*os.File) error
	ReplyError(ctx context.Context, name string, parameters any) error
	// FileDescriptors returns the file descriptors received with the
	// call; see UnaryCall.FileDescriptors for the ownership rules.
	FileDescriptors() []*os.File
}

// UpgradeCall is the server-side API for a successful connection upgrade.
type UpgradeCall interface {
	Method() string
	Decode(out any) error
	Accept(ctx context.Context, out any) (io.ReadWriteCloser, error)
	ReplyError(ctx context.Context, name string, parameters any) error
	// FileDescriptors returns the file descriptors received with the
	// call; see UnaryCall.FileDescriptors for the ownership rules.
	FileDescriptors() []*os.File
}

// ServerBuilder collects interfaces and configuration before the server is
// built.
type ServerBuilder struct {
	cfg    ServerConfig
	info   ServiceInfo
	ifaces map[string]*Interface
	order  []string
}

// Interface is a registration handle for one varlink interface. It pairs an
// interface description with the handlers implementing its methods.
type Interface struct {
	name        string
	description string
	idl         *idl.Interface

	unary   map[string]UnaryHandler
	stream  map[string]StreamHandler
	upgrade map[string]UpgradeHandler
}

// NewServerBuilder returns a builder with defensive defaults applied. The
// builtin org.varlink.service interface is registered automatically.
func NewServerBuilder(cfg ServerConfig) *ServerBuilder {
	if cfg.MaxFrameSize == 0 {
		cfg.MaxFrameSize = MaxFrameSizeDefault
	}
	b := &ServerBuilder{
		cfg:    cfg,
		ifaces: make(map[string]*Interface),
	}
	// The builtin service interface carries no handlers here; Build binds
	// its methods to the server instance under construction.
	iface := &Interface{
		name:        ServiceInterfaceName,
		description: ServiceInterfaceDescription,
		unary:       make(map[string]UnaryHandler),
		stream:      make(map[string]StreamHandler),
		upgrade:     make(map[string]UpgradeHandler),
	}
	if parsed, err := idl.Parse(ServiceInterfaceDescription); err == nil {
		iface.idl = parsed
	}
	b.ifaces[ServiceInterfaceName] = iface
	b.order = append(b.order, ServiceInterfaceName)
	return b
}

// SetInfo updates the metadata returned by org.varlink.service.GetInfo.
func (b *ServerBuilder) SetInfo(info ServiceInfo) {
	b.info = info
}

// Interface parses an interface description in the varlink interface
// definition language and registers it. The description is served verbatim
// through org.varlink.service.GetInterfaceDescription, and it is the
// authority for which methods the interface declares: calls to methods that
// are not declared are answered with org.varlink.service.MethodNotFound,
// while calls to declared but unimplemented methods are answered with
// org.varlink.service.MethodNotImplemented.
func (b *ServerBuilder) Interface(description string) (*Interface, error) {
	parsed, err := idl.Parse(description)
	if err != nil {
		return nil, err
	}
	if _, exists := b.ifaces[parsed.Name]; exists {
		return nil, ErrDuplicateInterface
	}
	iface := &Interface{
		name:        parsed.Name,
		description: description,
		idl:         parsed,
		unary:       make(map[string]UnaryHandler),
		stream:      make(map[string]StreamHandler),
		upgrade:     make(map[string]UpgradeHandler),
	}
	b.ifaces[iface.name] = iface
	b.order = append(b.order, iface.name)
	return iface, nil
}

// Name returns the name of the interface.
func (i *Interface) Name() string {
	return i.name
}

// Description returns the interface description the interface was
// registered with.
func (i *Interface) Description() string {
	return i.description
}

// Unary installs a unary handler for one declared method. Unary handlers
// also serve oneway calls to the method.
func (i *Interface) Unary(method string, handler UnaryHandler) error {
	if handler == nil {
		return ErrNilHandler
	}
	if err := i.checkRegistration(method); err != nil {
		return err
	}
	i.unary[method] = handler
	return nil
}

// Stream installs a streaming handler for one declared method. Calls to the
// method must set the more flag; otherwise they are answered with
// org.varlink.service.ExpectedMore.
func (i *Interface) Stream(method string, handler StreamHandler) error {
	if handler == nil {
		return ErrNilHandler
	}
	if err := i.checkRegistration(method); err != nil {
		return err
	}
	i.stream[method] = handler
	return nil
}

// Upgrade installs an upgrade handler for one declared method. Calls to the
// method must set the upgrade flag; otherwise they are answered with
// org.varlink.service.ExpectedUpgrade.
func (i *Interface) Upgrade(method string, handler UpgradeHandler) error {
	if handler == nil {
		return ErrNilHandler
	}
	if err := i.checkRegistration(method); err != nil {
		return err
	}
	i.upgrade[method] = handler
	return nil
}

// checkRegistration verifies that method is a declared method name of the
// interface that has no handler yet.
func (i *Interface) checkRegistration(method string) error {
	if method == "" || strings.Contains(method, ".") {
		return ErrInvalidMethod
	}
	if i.idl != nil {
		if _, declared := i.idl.Method(method); !declared {
			return ErrMethodNotDeclared
		}
	}
	if _, exists := i.unary[method]; exists {
		return ErrDuplicateMethod
	}
	if _, exists := i.stream[method]; exists {
		return ErrDuplicateMethod
	}
	if _, exists := i.upgrade[method]; exists {
		return ErrDuplicateMethod
	}
	return nil
}

// Server is an immutable runtime built from a ServerBuilder.
type Server struct {
	cfg    ServerConfig
	info   ServiceInfo
	ifaces map[string]*Interface
	order  []string
}

// Build freezes the registered interfaces and handlers into a Server.
func (b *ServerBuilder) Build() *Server {
	s := &Server{
		cfg:    b.cfg,
		info:   b.info,
		ifaces: make(map[string]*Interface, len(b.ifaces)),
		order:  append([]string(nil), b.order...),
	}
	for name, iface := range b.ifaces {
		s.ifaces[name] = iface.clone()
	}
	s.bindBuiltinService()
	return s
}

func (i *Interface) clone() *Interface {
	return &Interface{
		name:        i.name,
		description: i.description,
		idl:         i.idl,
		unary:       cloneMap(i.unary),
		stream:      cloneMap(i.stream),
		upgrade:     cloneMap(i.upgrade),
	}
}

// Config returns the server runtime configuration.
func (s *Server) Config() ServerConfig {
	return s.cfg
}

// Info returns the configured service metadata.
func (s *Server) Info() ServiceInfo {
	return s.info
}

// Interface returns the registration of the named interface.
func (s *Server) Interface(name string) (*Interface, bool) {
	iface, ok := s.ifaces[name]
	return iface, ok
}

// bindBuiltinService installs the org.varlink.service handlers on the
// freshly built server, so that they observe the final registration state.
func (s *Server) bindBuiltinService() {
	iface := s.ifaces[ServiceInterfaceName]
	iface.unary = make(map[string]UnaryHandler, 2)
	iface.unary["GetInfo"] = func(ctx context.Context, call UnaryCall) error {
		return s.handleGetInfo(ctx, call)
	}
	iface.unary["GetInterfaceDescription"] = func(ctx context.Context, call UnaryCall) error {
		return s.handleGetInterfaceDescription(ctx, call)
	}
}

func cloneMap[T any](in map[string]T) map[string]T {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]T, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// splitMethod splits a fully-qualified method name into its interface and
// method name.
func splitMethod(method string) (string, string, error) {
	i := strings.LastIndex(method, ".")
	if i <= 0 || i == len(method)-1 {
		return "", "", ErrInvalidMethod
	}
	return method[:i], method[i+1:], nil
}
