//go:build linux

package varlink

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// verifyPIDFDID validates LISTEN_PIDFDID: the inode number of a pidfd
// referring to this process. It reports whether the activation variables
// are addressed to this process. An empty value passes.
func verifyPIDFDID(value string) (bool, error) {
	if value == "" {
		return true, nil
	}
	want, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return false, fmt.Errorf("varlink: invalid LISTEN_PIDFDID: %w", err)
	}

	// A pidfd for ourselves; unlike PID 0, pidfd_open(2) requires a real PID.
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		// Without pidfd support the inode cannot be verified; assume the
		// variables were not passed to us.
		return false, nil
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, nil
	}
	return uint64(st.Ino) == want, nil
}
