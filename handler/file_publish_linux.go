//go:build linux

package handler

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func publishFileNoReplace(source, target string) error {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	// Older kernels and unsupported filesystems may still support atomic hard links.
	// Never substitute a check followed by an overwriting rename.
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return os.Link(source, target)
	}
	return &os.LinkError{Op: "renameat2", Old: source, New: target, Err: err}
}
