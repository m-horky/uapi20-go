// Package certification exposes the interface descriptions used by the
// interop test clients and servers.
package certification

import _ "embed"

// EchoDescription is the com.example.echo interface definition.
//
//go:embed com.example.echo.varlink
var EchoDescription string

// CertificationDescription is the org.varlink.certification interface
// definition.
//
//go:embed org.varlink.certification.varlink
var CertificationDescription string
