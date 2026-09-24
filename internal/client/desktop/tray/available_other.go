//go:build desktop && !linux

package tray

// available は macOS・Windows では常に true（OS の部品で出す）。
func available() (bool, string) { return true, "" }
