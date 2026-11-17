//go:build !linux

package handler

import "os"

func publishFileNoReplace(source, target string) error {
	return os.Link(source, target)
}
