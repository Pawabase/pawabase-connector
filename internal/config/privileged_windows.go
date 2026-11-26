//go:build windows

package config

import "golang.org/x/sys/windows"

// isPrivileged reports whether the process runs elevated (an Administrator shell, or a Windows service).
func isPrivileged() bool { return windows.GetCurrentProcessToken().IsElevated() }
