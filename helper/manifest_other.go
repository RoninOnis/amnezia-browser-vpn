//go:build !windows

package main

import "errors"

// На Linux/Darwin манифест кладётся в каталоги Firefox установщиками, а не самим
// бинарём: этот путь существует только для симметрии и ручной проверки JSON.
func registerManifest(path string) error {
	return errors.New("регистрация манифеста поддерживается только на Windows")
}

func canRegisterManifest() bool { return false }
