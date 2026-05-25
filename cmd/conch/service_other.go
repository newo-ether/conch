//go:build !windows

package main

func runService(name string, run func() error, stop func()) bool {
	return false
}
