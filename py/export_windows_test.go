package py

// ImageIDForTest returns the registered Windows image's id ("" if none).
func ImageIDForTest() string {
	if registeredFiles == nil {
		return ""
	}
	return registeredFiles.id
}

// SetImageIDForTest registers the same image files under another id, which a process
// started by the tests then extracts and holds as an image of another version would.
func SetImageIDForTest(id string) {
	registeredFiles = &windowsImage{id: id, files: registeredFiles.files}
}
