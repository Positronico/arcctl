package safety

// syncDir does nothing: Windows cannot open a directory for flushing, and NTFS
// journals the directory entry itself.
func syncDir(string) error { return nil }
