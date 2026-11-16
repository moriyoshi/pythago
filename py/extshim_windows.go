package py

// loadExtensionShim does nothing on Windows: the interpreter is a python3X.dll that the
// Windows loader loaded (image_windows.go), so extension modules, which import it by
// name, bind to it as they would under python.exe.
func loadExtensionShim(Config) error { return nil }
