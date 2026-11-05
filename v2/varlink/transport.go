package varlink

import (
	"context"
	"errors"
	"net"
	"strings"
)

// Address is a parsed varlink service address.
//
// Supported forms are unix sockets (`unix:/run/org.example`, with a leading
// `@` in the path denoting a socket in the Linux abstract namespace) and TCP
// (`tcp:host:port`, `tcp:127.0.0.1:port`, `tcp:[::1]:port`). Properties
// appended after a `;` separator are parsed but ignored, so that addresses
// can carry future extensions.
type Address struct {
	// Network is "unix" or "tcp".
	Network string
	// Path is the socket path of unix addresses. A leading `@` denotes an
	// abstract namespace socket.
	Path string
	// Host and Port are set for tcp addresses.
	Host string
	Port string
	// Props holds the `;`-separated properties of the address.
	Props map[string]string
}

// ParseAddress parses a varlink service address.
func ParseAddress(s string) (Address, error) {
	addr := Address{Props: map[string]string{}}

	location := s
	if i := strings.IndexByte(s, ';'); i >= 0 {
		location = s[:i]
		for _, prop := range strings.Split(s[i+1:], ";") {
			if prop == "" {
				continue
			}
			key, value, ok := strings.Cut(prop, "=")
			if !ok {
				return Address{}, errors.New("varlink: invalid address property")
			}
			addr.Props[key] = value
		}
	}

	network, rest, ok := strings.Cut(location, ":")
	if !ok {
		return Address{}, ErrInvalidAddress
	}
	switch network {
	case "unix":
		if rest == "" {
			return Address{}, ErrInvalidAddress
		}
		addr.Network = "unix"
		addr.Path = rest
	case "tcp":
		host, port, err := net.SplitHostPort(rest)
		if err != nil || port == "" {
			return Address{}, ErrInvalidAddress
		}
		addr.Network = "tcp"
		addr.Host = host
		addr.Port = port
	default:
		return Address{}, ErrInvalidAddress
	}
	return addr, nil
}

// String returns the canonical address URI.
func (a Address) String() string {
	location := ""
	switch a.Network {
	case "unix":
		location = "unix:" + a.Path
	case "tcp":
		location = "tcp:" + net.JoinHostPort(a.Host, a.Port)
	}
	if len(a.Props) == 0 {
		return location
	}
	props := make([]string, 0, len(a.Props))
	for key, value := range a.Props {
		props = append(props, key+"="+value)
	}
	return location + ";" + strings.Join(props, ";")
}

// Dial connects to the varlink service at address and returns a client for
// it. The connection is closed when the client is closed.
func Dial(ctx context.Context, address string) (*ConnClient, error) {
	return DialWithConfig(ctx, address, ClientConfig{})
}

// DialWithConfig is Dial with transport policy for the client.
func DialWithConfig(ctx context.Context, address string, cfg ClientConfig) (*ConnClient, error) {
	conn, err := DialConn(ctx, address)
	if err != nil {
		return nil, err
	}
	return NewClient(conn, cfg), nil
}

// DialConn connects to the varlink service at address and returns the raw
// network connection. It is meant for bridges and other proxies that need
// byte-level access to a varlink connection; the connection is tagged with
// the client role where the platform supports it.
func DialConn(ctx context.Context, address string) (net.Conn, error) {
	addr, err := ParseAddress(address)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, addr.Network, addr.dialTarget())
	if err != nil {
		return nil, err
	}
	tagClientConn(conn)
	return conn, nil
}

func (a Address) dialTarget() string {
	switch a.Network {
	case "tcp":
		return net.JoinHostPort(a.Host, a.Port)
	default:
		return a.Path
	}
}
