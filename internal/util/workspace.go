package util

import (
	"path/filepath"
	"strings"
)

// GetWorkspaceName safely extracts a usable name from a workspace path, handling root drives correctly.
func GetWorkspaceName(path string) string {
	base := filepath.Base(path)
	// On Windows, Base("C:\\") is "\\". On Linux, Base("/") is "/".
	if len(base) == 1 && (base[0] == filepath.Separator) {
		vol := filepath.VolumeName(path) // e.g., "C:"
		if vol != "" {
			return strings.TrimSuffix(vol, ":") // "C"
		}
		// Fallback for "/" on Unix-like systems, or if volume name is somehow empty.
		return "root"
	}
	return base
}

// IsRoot determines if the given path is a root directory (e.g., "C:\" or "/").
func IsRoot(path string) bool {
	// On Windows, VolumeName("C:\") is "C:", so after cleaning, its parent is itself.
	// On Unix, Dir("/") is "/".
	cleanPath := filepath.Clean(path)
	return filepath.Dir(cleanPath) == cleanPath
}
