//go:build windows

package main

// exeSuffix — Windows запускает только файлы с расширением .exe.
func exeSuffix() string { return ".exe" }
