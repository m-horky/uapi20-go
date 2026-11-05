package varlink

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
)

// activationFirstFD is the first file descriptor systemd passes to an
// activated service.
const activationFirstFD = 3

// ActivatedListeners returns the varlink listening sockets passed to this
// process at startup via the systemd socket activation protocol
// (sd_listen_fds(3)).
//
// The passed sockets MUST be identified by the LISTEN_FDNAMES environment
// variable naming the varlink socket "varlink"; if only a single file
// descriptor was passed, LISTEN_FDNAMES may be omitted. Per the protocol,
// the variables are ignored when LISTEN_PID does not name this process, or
// when LISTEN_PIDFDID does not match the inode of a pidfd referring to it.
//
// It returns nil when this process was not socket-activated.
//
// Note that socket activation may also pass established connection sockets
// rather than listening sockets; those cannot be returned here and cause an
// error. Serve them directly through Server.Serve.
func ActivatedListeners() ([]net.Listener, error) {
	pid := os.Getenv("LISTEN_PID")
	if pid == "" {
		return nil, nil
	}
	pidNum, err := strconv.Atoi(pid)
	if err != nil {
		return nil, fmt.Errorf("varlink: invalid LISTEN_PID: %w", err)
	}
	if pidNum != os.Getpid() {
		// Not for us; the variables MUST be ignored.
		return nil, nil
	}
	if ok, err := verifyPIDFDID(os.Getenv("LISTEN_PIDFDID")); err != nil {
		return nil, err
	} else if !ok {
		// Not for us; the variables MUST be ignored.
		return nil, nil
	}

	fds := os.Getenv("LISTEN_FDS")
	if fds == "" {
		return nil, nil
	}
	count, err := strconv.Atoi(fds)
	if err != nil {
		return nil, fmt.Errorf("varlink: invalid LISTEN_FDS: %w", err)
	}
	if count < 0 {
		return nil, fmt.Errorf("varlink: invalid LISTEN_FDS: %d", count)
	}
	if count == 0 {
		return nil, nil
	}

	indices, err := activationIndices(os.Getenv("LISTEN_FDNAMES"), count)
	if err != nil {
		return nil, err
	}

	listeners := make([]net.Listener, 0, len(indices))
	for _, i := range indices {
		file := os.NewFile(uintptr(activationFirstFD+i), fmt.Sprintf("varlink-activated-fd-%d", activationFirstFD+i))
		ln, err := net.FileListener(file)
		if err != nil {
			file.Close()
			return listeners, fmt.Errorf("varlink: activated fd %d is not a listening socket: %w", activationFirstFD+i, err)
		}
		listeners = append(listeners, ln)
	}
	return listeners, nil
}

// activationIndices selects the varlink sockets among the passed file
// descriptors: those named "varlink" in LISTEN_FDNAMES, or the single
// descriptor when no names are given.
func activationIndices(names string, count int) ([]int, error) {
	if names == "" {
		if count > 1 {
			return nil, fmt.Errorf("varlink: %d file descriptors passed but LISTEN_FDNAMES does not identify the varlink socket", count)
		}
		return []int{0}, nil
	}
	parts := splitActivationNames(names)
	if len(parts) != count {
		return nil, fmt.Errorf("varlink: LISTEN_FDNAMES has %d names but LISTEN_FDS is %d", len(parts), count)
	}
	var indices []int
	for i, name := range parts {
		if name == "varlink" {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return nil, errors.New("varlink: no file descriptor named \"varlink\" in LISTEN_FDNAMES")
	}
	return indices, nil
}

func splitActivationNames(names string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(names); i++ {
		if names[i] == ':' {
			parts = append(parts, names[start:i])
			start = i + 1
		}
	}
	return append(parts, names[start:])
}
