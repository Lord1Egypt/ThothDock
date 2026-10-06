package image

// SetFreeBytes replaces the free-space probe for a test and returns the restore function.
func SetFreeBytes(f func(path string) (uint64, bool)) (restore func()) {
	old := freeBytes
	freeBytes = f
	return func() { freeBytes = old }
}
