/*
Package varlink contains the public API for the v2 runtime.

The v2 design favors explicit call shapes over mutable request state:

  - unary calls return exactly one reply
  - stream calls return a receiver object
  - upgrades return a dedicated upgraded connection
  - pipelined unary/oneway calls use Batch

Transport policy is configured on clients and servers, while per-call
lifetimes continue to use context.Context.

On AF_UNIX stream sockets, file descriptors can be passed in SCM_RIGHTS
ancillary messages attached to the messages they belong to; see
InvokeFDs, OnewayFDs, RecvFDs, ReplyFDs, SendFDs and the AcceptFileDescriptors
configuration options. Other transports degrade gracefully with
ErrFileDescriptorsUnsupported. The subpackage varlink/httpbridge encapsulates
services behind the HTTP and WebSocket transports of the UAPI specification.
*/
package varlink
