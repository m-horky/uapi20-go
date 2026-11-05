//go:build linux

package varlink

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// childEnv marks the re-executed helper process that runs ActivatedListeners
// with descriptors fixed up by the parent, mirroring what a service manager
// does for socket activation.
const childEnv = "VARLINK_ACTIVATION_TEST_CHILD"

// TestMain dispatches to the helper process and otherwise runs the tests.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		activationChild()
		return
	}
	os.Exit(m.Run())
}

// activationChild sets the remaining activation variables that only the
// child knows (its own PID and pidfd inode), calls ActivatedListeners, and
// reports the outcome on stdout for the parent to assert on.
func activationChild() {
	if os.Getenv("VARLINK_ACTIVATION_TEST_PID") != "keep" {
		_ = os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	}
	if mode := os.Getenv("VARLINK_ACTIVATION_TEST_PIDFDID"); mode != "" {
		inode, ok := selfPidfdInode()
		if !ok {
			fmt.Println("SKIP")
			return
		}
		if mode == "stale" {
			inode++
		}
		_ = os.Setenv("LISTEN_PIDFDID", strconv.FormatUint(inode, 10))
	}

	listeners, err := ActivatedListeners()
	if err != nil {
		fmt.Printf("ERR %v\n", err)
		return
	}
	if listeners == nil {
		fmt.Println("NIL")
		return
	}
	for _, ln := range listeners {
		defer ln.Close()
	}
	fmt.Printf("OK %d\n", len(listeners))
}

// runActivationChild re-executes the test binary with the given extra files
// (arriving as descriptors starting at 3) and activation variables, and
// returns the helper's report line.
func runActivationChild(t *testing.T, extra []*os.File, vars map[string]string) string {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.ExtraFiles = extra
	cmd.Env = append(os.Environ(), childEnv+"=1")
	for key, value := range vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper process: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Fatalf("helper process output = %q, want a single report line", string(out))
	}
	return lines[0]
}

func listenerFiles(t *testing.T, ln *net.UnixListener, n int) []*os.File {
	t.Helper()
	files := make([]*os.File, n)
	for i := range files {
		f, err := ln.File()
		if err != nil {
			t.Fatalf("listener File() error = %v", err)
		}
		files[i] = f
		t.Cleanup(func() { _ = f.Close() })
	}
	return files
}

func TestActivatedListenersSingleFD(t *testing.T) {
	ln, err := listenUnixAt(filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	report := runActivationChild(t, listenerFiles(t, ln, 1), map[string]string{
		"LISTEN_FDS": "1",
	})
	if report != "OK 1" {
		t.Errorf("ActivatedListeners() report = %q, want %q", report, "OK 1")
	}
}

func TestActivatedListenersFDNames(t *testing.T) {
	ln, err := listenUnixAt(filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	report := runActivationChild(t, listenerFiles(t, ln, 2), map[string]string{
		"LISTEN_FDS":     "2",
		"LISTEN_FDNAMES": "other:varlink",
	})
	if report != "OK 1" {
		t.Errorf("ActivatedListeners() report = %q, want %q", report, "OK 1")
	}
}

func TestActivatedListenersMultipleFDsWithoutNames(t *testing.T) {
	ln, err := listenUnixAt(filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	report := runActivationChild(t, listenerFiles(t, ln, 2), map[string]string{
		"LISTEN_FDS": "2",
	})
	if !strings.HasPrefix(report, "ERR ") {
		t.Errorf("ActivatedListeners() report = %q, want an error for ambiguous descriptors", report)
	}
}

func TestActivatedListenersNoVarlinkName(t *testing.T) {
	ln, err := listenUnixAt(filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	report := runActivationChild(t, listenerFiles(t, ln, 1), map[string]string{
		"LISTEN_FDS":     "1",
		"LISTEN_FDNAMES": "other",
	})
	if !strings.HasPrefix(report, "ERR ") {
		t.Errorf("ActivatedListeners() report = %q, want an error for a missing varlink socket", report)
	}
}

func TestActivatedListenersPIDMismatch(t *testing.T) {
	ln, err := listenUnixAt(filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	report := runActivationChild(t, listenerFiles(t, ln, 1), map[string]string{
		"LISTEN_PID":                  "1",
		"LISTEN_FDS":                  "1",
		"VARLINK_ACTIVATION_TEST_PID": "keep",
	})
	if report != "NIL" {
		t.Errorf("ActivatedListeners() report = %q, want %q", report, "NIL")
	}
}

func TestActivatedListenersPIDFDID(t *testing.T) {
	ln, err := listenUnixAt(filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	vars := map[string]string{
		"LISTEN_FDS":                      "1",
		"VARLINK_ACTIVATION_TEST_PIDFDID": "ok",
	}
	report := runActivationChild(t, listenerFiles(t, ln, 1), vars)
	switch report {
	case "OK 1":
	case "SKIP":
		t.Skip("pidfd not available")
	default:
		t.Errorf("ActivatedListeners() report = %q, want %q or SKIP", report, "OK 1")
	}

	vars["VARLINK_ACTIVATION_TEST_PIDFDID"] = "stale"
	report = runActivationChild(t, listenerFiles(t, ln, 1), vars)
	if report != "NIL" {
		t.Errorf("ActivatedListeners() with stale PIDFDID report = %q, want %q", report, "NIL")
	}
}

func TestActivatedListenersNotActivated(t *testing.T) {
	listeners, err := ActivatedListeners()
	if err != nil {
		t.Fatalf("ActivatedListeners() error = %v", err)
	}
	if listeners != nil {
		t.Fatalf("ActivatedListeners() = %v, want nil", listeners)
	}
}

// TestActivatedListenerServesFirstClient verifies that a listener obtained
// through socket activation accepts and serves connections.
func TestActivatedListenerServesFirstClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")
	ln, err := listenUnixAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	server := NewServerBuilder(ServerConfig{}).Build()
	listener := &Listener{server: server, ln: ln}
	go func() { _ = listener.Serve(context.Background()) }()

	client, err := Dial(context.Background(), "unix:"+path)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client.Close()
}

func listenUnixAt(path string) (*net.UnixListener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		ln.Close()
		return nil, net.InvalidAddrError("not a unix listener")
	}
	return ul, nil
}

func selfPidfdInode() (uint64, bool) {
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return 0, false
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, false
	}
	return st.Ino, true
}
