package varlink

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryRejectsInvalidInterfaceNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"",
		"nosuchshape",
		".org.example",
		"org.example.",
		"org..example",
		"org.example..Ping",
		"/org/example",
		"org/example",
		"../org.example",
		"org.example/../other",
		"1org.example",
		"org.-example",
		"org.example-",
	} {
		if _, err := registerInRegistry(dir, []string{name}, "socket"); !errors.Is(err, ErrInvalidInterface) {
			t.Errorf("registerInRegistry(%q) error = %v, want ErrInvalidInterface", name, err)
		}
		if err := unregisterFromRegistry(dir, []string{name}); !errors.Is(err, ErrInvalidInterface) {
			t.Errorf("unregisterFromRegistry(%q) error = %v, want ErrInvalidInterface", name, err)
		}
		if name != "" {
			if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name))); !os.IsNotExist(err) {
				t.Errorf("registerInRegistry(%q) created an entry outside the registry directory", name)
			}
		}
	}
}
