package common

import "strings"

// DeviceName generates a deterministic LXD/Incus device name from a container mount path.
func DeviceName(containerPath string) string {
	name := "mount-" + strings.ReplaceAll(containerPath, "/", "-")
	return strings.Trim(name, "-")
}

// IsHex reports whether the string s consists only of hexadecimal characters.
func IsHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
