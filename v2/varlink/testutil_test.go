package varlink

import (
	"os"
	"path/filepath"
	"testing"
)

// newUnixTestSocketPath returns a fresh socket path inside a short temporary
// directory. It deliberately does not use t.TempDir(): that embeds the full
// test name in the path, and macOS limits a unix socket path to 104 bytes
// including the terminating NUL. Long test names would therefore overflow
// sockaddr_un.sun_path and make Listen fail with "bind: invalid argument".
func newUnixTestSocketPath(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "varlink-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "sock")
}
