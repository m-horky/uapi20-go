package varlink

// CallOption configures optional behavior of one client call. It is the
// opt-in mechanism for file descriptor passing: calls that do not need
// descriptors take no options and have no descriptor-aware results, so a
// caller that does not opt in cannot leak descriptors a peer attaches to
// its replies.
type CallOption func(*callOptions)

// callOptions carries the effective options of one call: the files to send
// with the message, and the destination receiving the files that come back
// with its reply.
type callOptions struct {
	files Files
	reply *Files
}

// SendFiles returns a CallOption that sends files with the message,
// attached in one SCM_RIGHTS ancillary message. The descriptors are
// duplicated by the kernel when the message is written, so the caller may
// close its copies as soon as the call returns; files queued in a batch
// must instead stay open until Batch.Send has written them. Sending files
// requires an AF_UNIX connection; otherwise the call fails with
// ErrFileDescriptorsUnsupported.
//
// The JSON parameters of the message reference the descriptors by their
// index in files; which parameter fields carry indices is defined by the
// interface, not by the transport.
func SendFiles(files Files) CallOption {
	return func(o *callOptions) {
		o.files = files
	}
}

// ReceiveFiles returns a CallOption that stores the file descriptors
// received with the reply in *dst, transferring their ownership to the
// caller. It is only valid on operations that receive a reply; the
// descriptors are stored only when the call succeeds, and descriptors
// received with an error reply (or by a call that passed no ReceiveFiles)
// are closed by the library, so they cannot leak. Receiving descriptors
// requires ClientConfig.AcceptFileDescriptors; without it the kernel
// discards them and *dst stays nil.
func ReceiveFiles(dst *Files) CallOption {
	return func(o *callOptions) {
		o.reply = dst
	}
}

// newCallOptions applies opts in order, later options winning.
func newCallOptions(opts []CallOption) callOptions {
	var o callOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// requireNoReply rejects reply-destination options on operations that
// cannot receive a reply.
func (o callOptions) requireNoReply() error {
	if o.reply != nil {
		return ErrInvalidCallOption
	}
	return nil
}

// requireNoFiles rejects send options on operations that cannot send.
func (o callOptions) requireNoFiles() error {
	if o.files != nil {
		return ErrInvalidCallOption
	}
	return nil
}

// deliver hands the files received with a successful reply to the
// destination configured by ReceiveFiles, or closes them when the caller
// did not opt in to receiving them.
func (o callOptions) deliver(files Files) {
	if o.reply != nil {
		*o.reply = files
		return
	}
	files.Close()
}
