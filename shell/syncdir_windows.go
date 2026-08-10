//go:build windows

package shell

// Windows does not expose directory handles through os.File.Sync. The snapshot file itself is
// flushed before MoveFile/rename, and the .bak/.tmp recovery fence covers interrupted replacement.
func syncDirectory(string) error {
	return nil
}
