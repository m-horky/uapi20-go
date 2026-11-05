package varlink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// staleSocketProbeTimeout bounds the connect attempt used to detect
// whether a socket file at a listen path is still being served.
const staleSocketProbeTimeout = time.Second

// Listener accepts connections for a Server on a service address. Serve
// processes connections until the context is canceled or Close is called.
type Listener struct {
	server *Server
	ln     net.Listener

	// path is the filesystem path of a unix entrypoint socket to remove
	// when the listener closes; "" for other address types.
	path string

	closeOnce sync.Once
	closeErr  error
}

// Listen creates a listener for the service on address.
//
// For unix addresses, a stale socket file at the path is removed before
// listening; a path occupied by a file that is not a socket is an error.
// Abstract namespace sockets require no cleanup.
func (s *Server) Listen(ctx context.Context, address string) (*Listener, error) {
	addr, err := ParseAddress(address)
	if err != nil {
		return nil, err
	}

	switch addr.Network {
	case "unix":
		return s.listenUnix(addr)
	case "tcp":
		ln, err := net.Listen("tcp", net.JoinHostPort(addr.Host, addr.Port))
		if err != nil {
			return nil, err
		}
		return &Listener{server: s, ln: ln}, nil
	default:
		return nil, ErrInvalidAddress
	}
}

func (s *Server) listenUnix(addr Address) (*Listener, error) {
	path := addr.Path
	if path[0] != '@' {
		if err := removeStaleSocket(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Unless the service opted into receiving file descriptors, turn off
	// their reception on the listening socket where the kernel supports
	// it, so that unwanted descriptors are not queued.
	if !s.cfg.AcceptFileDescriptors {
		if ul, ok := ln.(*net.UnixListener); ok {
			disablePassRights(ul)
		}
	}
	// A path-bound listening socket carries both the listen role on the
	// socket inode and the entrypoint role on the file-system inode.
	tagUnixListener(ln, path)
	return &Listener{server: s, ln: ln, path: path}, nil
}

// removeStaleSocket removes a leftover socket file at path. A file that is
// not a socket is an error, and so is a socket that another process is
// still serving: the file is only removed when connecting to it is
// refused, so that a second instance cannot hijack the address of a
// running service.
func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSocket == 0:
		return fmt.Errorf("%w: %s is not a socket", ErrAddressInUse, path)
	}
	conn, err := net.DialTimeout("unix", path, staleSocketProbeTimeout)
	if err == nil {
		conn.Close()
		return fmt.Errorf("%w: %s is served by another process", ErrAddressInUse, path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		// The socket cannot be probed (e.g. permission denied); leave it
		// in place rather than risk removing a live entrypoint.
		return fmt.Errorf("%w: %s cannot be probed: %v", ErrAddressInUse, path, err)
	}
	return os.Remove(path)
}

// Serve accepts connections and serves each of them on its own goroutine
// until the context is canceled or the listener is closed. It returns the
// context error if the context was canceled, and nil otherwise. Connection
// level errors are reported through the server's Logger and ErrorHandler.
func (l *Listener) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-ctx.Done()
		l.Close()
	}()

	// connections beyond the configured limit are rejected.
	var slots chan struct{}
	if max := l.server.cfg.MaxConnections; max > 0 {
		slots = make(chan struct{}, max)
	}

	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return nil
			}
			return err
		}
		if slots != nil {
			select {
			case slots <- struct{}{}:
			default:
				if l.server.cfg.Logger != nil {
					l.server.cfg.Logger.Printf("varlink: connection limit (%d) reached, rejecting %s", cap(slots), conn.RemoteAddr())
				}
				conn.Close()
				continue
			}
		}
		go func() {
			if slots != nil {
				defer func() { <-slots }()
			}
			if err := l.server.Serve(ctx, conn); err != nil {
				l.server.reportError(ctx, err)
			}
		}()
	}
}

// Addr returns the listener's network address.
func (l *Listener) Addr() net.Addr {
	return l.ln.Addr()
}

// Close closes the listener and removes a unix entrypoint socket from the
// filesystem. It is safe to call multiple times.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		l.closeErr = l.ln.Close()
		if l.path != "" && l.path[0] != '@' {
			if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) && l.closeErr == nil {
				l.closeErr = err
			}
		}
	})
	return l.closeErr
}
