package note

// IsPureRenoteForTest exposes isPureRenote to package-external tests.
func IsPureRenoteForTest(in CreateInput) bool {
	return isPureRenote(in)
}

// SafeGoForTest exposes safeGo to package-external tests.
func SafeGoForTest(fn func()) { safeGo(fn) }
