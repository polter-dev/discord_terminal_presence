//go:build !windows

package service

func canonicalWindowsExecutable(value string) (string, error) {
	return normalizeResolvedWindowsPath(value), nil
}

func sameWindowsFileIdentity(_, _ string) (same, determined bool) {
	return false, false
}
