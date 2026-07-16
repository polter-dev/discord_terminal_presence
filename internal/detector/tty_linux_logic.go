package detector

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type linuxTTYResolver struct {
	devices  map[uint64]string
	readStat func(pid int32) ([]byte, error)
	err      error
}

func (r *linuxTTYResolver) Resolve(pid int32) (TTYResolution, error) {
	if r.err != nil {
		return TTYResolution{}, r.err
	}
	stat, err := r.readStat(pid)
	if err != nil {
		return TTYResolution{}, err
	}
	ttyNumber, err := parseLinuxTTYNumber(stat)
	if err != nil {
		return TTYResolution{}, err
	}
	if ttyNumber == 0 {
		return TTYResolution{NoTTY: true}, nil
	}
	path, ok := r.devices[uint64(uint32(ttyNumber))]
	if !ok {
		return TTYResolution{}, errors.New("controlling tty device not found")
	}
	return TTYResolution{Path: path}, nil
}

func parseLinuxTTYNumber(stat []byte) (int32, error) {
	text := string(stat)
	openingParen := strings.IndexByte(text, '(')
	closingParen := strings.LastIndexByte(text, ')')
	if openingParen < 0 || closingParen < openingParen {
		return 0, errors.New("malformed /proc stat: missing command terminator")
	}
	// Fields after comm begin with field 3 (state), so tty_nr (field 7) is
	// the fifth whitespace-delimited value. Parsing after the last ')' is
	// essential because comm may itself contain spaces and parentheses.
	fields := strings.Fields(text[closingParen+1:])
	if len(fields) < 5 {
		return 0, errors.New("malformed /proc stat: missing tty_nr")
	}
	value, err := strconv.ParseInt(fields[4], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse /proc stat tty_nr: %w", err)
	}
	return int32(value), nil
}

type linuxMount struct {
	path    string
	fsType  string
	options map[string]struct{}
}

func parseLinuxMounts(contents []byte) ([]linuxMount, error) {
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	mounts := make([]linuxMount, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, errors.New("malformed /proc/mounts entry")
		}
		path, err := unescapeLinuxMountField(fields[1])
		if err != nil {
			return nil, err
		}
		options := make(map[string]struct{})
		for _, option := range strings.Split(fields[3], ",") {
			options[option] = struct{}{}
		}
		mounts = append(mounts, linuxMount{path: filepath.Clean(path), fsType: fields[2], options: options})
	}
	return mounts, nil
}

func unescapeLinuxMountField(value string) (string, error) {
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			decoded.WriteByte(value[i])
			continue
		}
		if i+3 >= len(value) {
			return "", errors.New("malformed escape in /proc/mounts")
		}
		octal := value[i+1 : i+4]
		parsed, err := strconv.ParseUint(octal, 8, 8)
		if err != nil {
			return "", errors.New("malformed escape in /proc/mounts")
		}
		decoded.WriteByte(byte(parsed))
		i += 3
	}
	return decoded.String(), nil
}

type linuxTTYAtimeSource struct {
	mounts   []linuxMount
	mountErr error
	stat     func(path string) (time.Time, error)
}

func (s linuxTTYAtimeSource) Atime(path string) (time.Time, error) {
	if s.mountErr != nil {
		return time.Time{}, s.mountErr
	}
	mount, ok := coveringDevptsMount(path, s.mounts)
	if !ok || !trustworthyLinuxAtime(mount.options) {
		return time.Time{}, errors.New("tty mount does not provide trustworthy atime")
	}
	return s.stat(path)
}

func coveringDevptsMount(path string, mounts []linuxMount) (linuxMount, bool) {
	path = filepath.Clean(path)
	var best linuxMount
	found := false
	for _, mount := range mounts {
		if mount.fsType != "devpts" || !pathWithinMount(path, mount.path) {
			continue
		}
		if !found || len(mount.path) > len(best.path) {
			best, found = mount, true
		}
	}
	return best, found
}

func pathWithinMount(path, mount string) bool {
	if path == mount {
		return true
	}
	if mount == string(filepath.Separator) {
		return strings.HasPrefix(path, mount)
	}
	return strings.HasPrefix(path, mount+string(filepath.Separator))
}

func trustworthyLinuxAtime(options map[string]struct{}) bool {
	has := func(option string) bool {
		_, ok := options[option]
		return ok
	}
	// Decision matrix: strictatime is trustworthy, as is an explicit atime
	// option. noatime and relatime suppress per-read updates; nodiratime mixed
	// with an enabling option is treated as ambiguous. Any conflicting,
	// missing, or unrecognised policy fails open by withholding atime.
	if has("noatime") || has("relatime") || has("nodiratime") {
		return false
	}
	return has("strictatime") || has("atime")
}
