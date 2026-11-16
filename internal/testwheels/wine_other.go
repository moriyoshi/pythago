//go:build !windows

package testwheels

// UnderWine reports whether the process runs under Wine: never off Windows.
func UnderWine() bool { return false }
