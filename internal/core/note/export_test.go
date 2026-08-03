package note

// SafeGoForTest exposes safeGo to package-external tests.
func SafeGoForTest(fn func()) { safeGo(fn) }
