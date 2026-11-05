//go:build !linux

package varlink

// verifyPIDFDID validates LISTEN_PIDFDID, which is Linux-specific. Without
// pidfd support the value cannot be verified, so the activation variables
// are treated as not addressed to this process.
func verifyPIDFDID(value string) (bool, error) {
	if value == "" {
		return true, nil
	}
	return false, nil
}
