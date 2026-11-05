package varlink

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RegisterInRegistry creates symlinks in the registry directory
// (/run/varlink/registry) named after each implemented interface, pointing
// at the service's entrypoint socket. A service implementing multiple
// well-known interfaces is registered under each of them. It returns the
// names it registered.
//
// The registry allows clients to discover the well-known, public Varlink
// services on a system, but it does not list all of them: services may bind
// their entrypoint sockets elsewhere.
func RegisterInRegistry(interfaces []string, socketPath string) ([]string, error) {
	return registerInRegistry(RegistryDirectory, interfaces, socketPath)
}

// UnregisterFromRegistry removes the registry symlinks previously created
// for the interfaces.
func UnregisterFromRegistry(interfaces []string) error {
	return unregisterFromRegistry(RegistryDirectory, interfaces)
}

func registerInRegistry(dir string, interfaces []string, socketPath string) ([]string, error) {
	for _, name := range interfaces {
		if !isValidInterfaceName(name) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidInterface, name)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("varlink: create registry directory: %w", err)
	}
	var registered []string
	for _, name := range interfaces {
		if err := os.Symlink(socketPath, filepath.Join(dir, name)); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return registered, fmt.Errorf("varlink: register %s: %w", name, err)
		}
		registered = append(registered, name)
	}
	return registered, nil
}

func unregisterFromRegistry(dir string, interfaces []string) error {
	for _, name := range interfaces {
		if !isValidInterfaceName(name) {
			return fmt.Errorf("%w: %q", ErrInvalidInterface, name)
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("varlink: unregister %s: %w", name, err)
		}
	}
	return nil
}

// isValidInterfaceName reports whether name is a well-formed varlink
// interface name in reverse-domain notation: at least two dot-separated
// components, the first starting with a letter and the others optionally
// with a digit, hyphens only between alphanumeric characters. The check
// keeps names free of path separators and traversal sequences, so that
// they cannot escape the registry directory.
func isValidInterfaceName(name string) bool {
	components := strings.Split(name, ".")
	if len(components) < 2 {
		return false
	}
	for i, component := range components {
		if !isValidNameComponent(component, i == 0) {
			return false
		}
	}
	return true
}

func isValidNameComponent(component string, first bool) bool {
	if component == "" {
		return false
	}
	if !isNameLetter(component[0]) && !(!first && isNameDigit(component[0])) {
		return false
	}
	for i := 1; i < len(component); i++ {
		if isNameAlnum(component[i]) {
			continue
		}
		// A hyphen must be followed by an alphanumeric character.
		if component[i] == '-' && i+1 < len(component) && isNameAlnum(component[i+1]) {
			continue
		}
		return false
	}
	return true
}

func isNameAlnum(c byte) bool {
	return isNameLetter(c) || isNameDigit(c)
}

func isNameLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
