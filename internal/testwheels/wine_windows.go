package testwheels

import "syscall"

// UnderWine reports whether the process runs under Wine, whose ntdll exports
// wine_get_version.
func UnderWine() bool {
	return syscall.NewLazyDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil
}
