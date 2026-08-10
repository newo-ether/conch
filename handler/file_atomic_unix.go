//go:build !windows

package handler

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

func syncFileDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
