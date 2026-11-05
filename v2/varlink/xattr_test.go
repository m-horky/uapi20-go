//go:build linux

package varlink

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// getXattr reads the user.varlink attribute of path.
func getXattr(path string) (string, error) {
	// A 32-byte buffer suffices for the defined roles.
	buf := make([]byte, 32)
	n, err := unix.Getxattr(path, XattrUserVarlink, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// TestTagPath verifies entrypoint tagging on a regular file-system inode,
// standing in for a socket inode. Skipped where user.* xattrs are not
// supported by the file system.
func TestTagPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entrypoint")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tagPath(path, RoleEntrypoint)

	value, err := getXattr(path)
	if err != nil {
		t.Skipf("user.* xattrs not supported: %v", err)
	}
	if string(value) != RoleEntrypoint {
		t.Fatalf("user.varlink = %q, want %q", value, RoleEntrypoint)
	}
}

// TestListenTagsEntrypoint verifies that a path-bound listener created
// through Server.Listen carries the entrypoint attribute.
func TestListenTagsEntrypoint(t *testing.T) {
	server := NewServerBuilder(ServerConfig{}).Build()
	listener, err := server.Listen(t.Context(), "unix:"+filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	value, err := getXattr(listener.path)
	if err != nil {
		t.Skipf("user.* xattrs not supported: %v", err)
	}
	if string(value) != RoleEntrypoint {
		t.Fatalf("user.varlink = %q, want %q", value, RoleEntrypoint)
	}
}

func TestRegistry(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "sock")

	registered, err := registerInRegistry(dir, []string{"org.example.one", "org.example.two"}, socketPath)
	if err != nil {
		t.Fatalf("registerInRegistry() error = %v", err)
	}
	if len(registered) != 2 {
		t.Fatalf("registerInRegistry() = %v, want 2 names", registered)
	}
	for _, name := range registered {
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("Readlink(%s) error = %v", name, err)
		}
		if target != socketPath {
			t.Fatalf("registry entry %s -> %s, want %s", name, target, socketPath)
		}
	}

	if err := unregisterFromRegistry(dir, registered); err != nil {
		t.Fatalf("unregisterFromRegistry() error = %v", err)
	}
	for _, name := range registered {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("registry entry %s still present", name)
		}
	}

	// Unregistering unknown names is not an error.
	if err := unregisterFromRegistry(dir, []string{"org.example.missing"}); err != nil {
		t.Fatalf("unregisterFromRegistry() error = %v", err)
	}
}
