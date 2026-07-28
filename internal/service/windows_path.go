package service

import (
	"os"
	"path"
	"strings"
)

func normalizeWindowsPath(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	value = expandWindowsEnvironment(value)
	return normalizeResolvedWindowsPath(value)
}

func normalizeResolvedWindowsPath(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	value = strings.ReplaceAll(value, "/", `\`)
	if value == "" {
		return ""
	}

	if len(value) >= 3 && value[1] == ':' && value[2] == '\\' {
		drive := value[:2]
		remainder := strings.TrimLeft(value[2:], `\`)
		cleaned := path.Clean("/" + strings.ReplaceAll(remainder, `\`, "/"))
		return drive + strings.ReplaceAll(cleaned, "/", `\`)
	}

	unc := strings.HasPrefix(value, `\\`)
	cleaned := strings.ReplaceAll(path.Clean(strings.ReplaceAll(value, `\`, "/")), "/", `\`)
	if unc && !strings.HasPrefix(cleaned, `\\`) {
		cleaned = `\` + cleaned
	}
	if len(cleaned) == 2 && cleaned[1] == ':' {
		cleaned += `\`
	}
	return cleaned
}

func expandWindowsEnvironment(value string) string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		name, contents, found := strings.Cut(entry, "=")
		if found {
			env[strings.ToUpper(name)] = contents
		}
	}
	return expandPercentVariables(value, func(name string) (string, bool) {
		replacement, ok := env[strings.ToUpper(name)]
		return replacement, ok
	})
}

func expandPercentVariables(value string, lookup func(string) (string, bool)) string {
	var expanded strings.Builder
	for {
		start := strings.IndexByte(value, '%')
		if start < 0 {
			expanded.WriteString(value)
			return expanded.String()
		}
		endOffset := strings.IndexByte(value[start+1:], '%')
		if endOffset < 0 {
			expanded.WriteString(value)
			return expanded.String()
		}
		end := start + 1 + endOffset
		expanded.WriteString(value[:start])
		name := value[start+1 : end]
		if replacement, ok := lookup(name); ok {
			expanded.WriteString(replacement)
		} else {
			expanded.WriteString(value[start : end+1])
		}
		value = value[end+1:]
	}
}
