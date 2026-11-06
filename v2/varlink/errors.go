package varlink

import (
	"errors"

	"github.com/m-horky/uapi20-go/v2/internal/codec"
)

// Well-known error names defined by the org.varlink.service interface. These
// generic errors may be returned by any method of any interface.
const (
	// ErrorInterfaceNotFound is returned when a method is called on an
	// interface the service does not implement.
	ErrorInterfaceNotFound = "org.varlink.service.InterfaceNotFound"
	// ErrorMethodNotFound is returned when a method is called that the
	// known interface does not define.
	ErrorMethodNotFound = "org.varlink.service.MethodNotFound"
	// ErrorMethodNotImplemented is returned when the interface defines the
	// method, but the service does not implement it.
	ErrorMethodNotImplemented = "org.varlink.service.MethodNotImplemented"
	// ErrorInvalidParameter is returned when one of the passed parameters
	// is invalid (missing, wrong type, unknown, or out of range).
	ErrorInvalidParameter = "org.varlink.service.InvalidParameter"
	// ErrorPermissionDenied is returned when the client is denied access
	// to the method or resource.
	ErrorPermissionDenied = "org.varlink.service.PermissionDenied"
	// ErrorExpectedMore is returned when a method has to be called with
	// the more flag set, but was not.
	ErrorExpectedMore = "org.varlink.service.ExpectedMore"
	// ErrorExpectedUpgrade is returned when a method has to be called with
	// the upgrade flag set, but was not.
	ErrorExpectedUpgrade = "org.varlink.service.ExpectedUpgrade"
)

// ServiceInterfaceName is the name of the interface every varlink service
// should implement.
const ServiceInterfaceName = "org.varlink.service"

var (
	// ErrProtocolViolation is returned when a peer sends a structurally invalid varlink message.
	ErrProtocolViolation = errors.New("varlink: protocol violation")
	// ErrUnexpectedReply is returned when the peer sends a reply shape that does not match the request.
	ErrUnexpectedReply = errors.New("varlink: unexpected reply")
	// ErrMalformedMessage is returned when the peer sends a message that violates the
	// wire protocol and the connection has to be closed. There is no
	// well-defined way to reply to such a message.
	ErrMalformedMessage = codec.ErrMalformedMessage
	// ErrFrameTooLarge is returned when a peer sends a message larger than the configured limit.
	ErrFrameTooLarge = codec.ErrFrameTooLarge
	// ErrStreamClosed is returned when receiving from a closed stream.
	ErrStreamClosed = errors.New("varlink: stream closed")
	// ErrDuplicateInterface is returned when the same interface is registered twice.
	ErrDuplicateInterface = errors.New("varlink: interface already registered")
	// ErrDuplicateMethod is returned when the same method is registered more than once.
	ErrDuplicateMethod = errors.New("varlink: method already registered")
	// ErrInvalidInterface is returned when an interface name is malformed.
	ErrInvalidInterface = errors.New("varlink: invalid interface name")
	// ErrInvalidMethod is returned when a fully qualified method name is malformed.
	ErrInvalidMethod = errors.New("varlink: invalid method name")
	// ErrInvalidAddress is returned when a service address is malformed.
	ErrInvalidAddress = errors.New("varlink: invalid address")
	// ErrAddressInUse is returned when a unix socket path exists but is not a socket.
	ErrAddressInUse = errors.New("varlink: address in use")
	// ErrNilHandler is returned when a nil handler is registered.
	ErrNilHandler = errors.New("varlink: nil handler")
	// ErrInterfaceNotFound is returned when a method references an unknown interface.
	ErrInterfaceNotFound = errors.New("varlink: interface not found")
	// ErrMethodNotFound is returned when a method lookup misses within a known interface.
	ErrMethodNotFound = errors.New("varlink: method not found")
	// ErrMethodNotDeclared is returned when a handler is registered for a method that the
	// interface description does not declare.
	ErrMethodNotDeclared = errors.New("varlink: method not declared by interface")
	// ErrBatchSent is returned when a batch is reused after Send.
	ErrBatchSent = errors.New("varlink: batch already sent")
)

// IsServiceError reports whether err is a remote error with the given
// fully-qualified error name.
func IsServiceError(err error, name string) bool {
	var remote *RemoteError
	return errors.As(err, &remote) && remote.Name == name
}

// ReplyError represents a protocol or decode failure observed while receiving a reply.
type ReplyError struct {
	Method string
	Err    error
}

func (e *ReplyError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Method == "" {
		return "varlink reply error: " + e.Err.Error()
	}
	return "varlink reply error for " + e.Method + ": " + e.Err.Error()
}

func (e *ReplyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
