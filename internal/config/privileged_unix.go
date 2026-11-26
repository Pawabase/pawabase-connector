//go:build !windows

package config

import "os"

// isPrivileged reports whether the process runs as root (a system service, or sudo).
func isPrivileged() bool { return os.Geteuid() == 0 }
