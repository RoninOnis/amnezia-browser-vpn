//go:build !windows

package main

// exeSuffix — на Unix расширение не нужно.
func exeSuffix() string { return "" }
