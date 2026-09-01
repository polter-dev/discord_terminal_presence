package main

import (
	"fmt"
	"strings"

	"github.com/polter-dev/discord_terminal_presence/internal/terminaltext"
)

type outputField struct {
	label string
	value string
}

type outputSection struct {
	header string
	fields []outputField
}

// formatSections renders plain, copy-friendly key/value groups. Labels are
// aligned within each group so an unusually long label in one section does not
// waste space in every other section.
func formatSections(title string, sections ...outputSection) string {
	var b strings.Builder
	b.WriteString(terminaltext.Sanitize(title))
	b.WriteByte('\n')

	for _, section := range sections {
		if section.header != "" {
			b.WriteByte('\n')
			b.WriteString(terminaltext.Sanitize(section.header))
			b.WriteByte('\n')
		}

		width := 0
		for _, field := range section.fields {
			width = max(width, len(terminaltext.Sanitize(field.label)))
		}
		for _, field := range section.fields {
			fmt.Fprintf(&b, "  %-*s  %s\n", width, terminaltext.Sanitize(field.label), displayValue(field.value))
		}
	}

	return b.String()
}

// naPlaceholder fills a status-table cell that has no value to show.
//
// It reads "not applicable in this row", not "value missing", which is why it
// is the literal text rather than a dash. `n/a` is also what displayValue
// already accepts as *input* and normalizes, so input and output now agree, and
// it survives any terminal font or encoding a dash may not (issue #585).
const naPlaceholder = "n/a"

func displayValue(value string) string {
	value = strings.TrimSpace(terminaltext.SanitizeSingleLine(value))
	if value == "" || strings.EqualFold(value, naPlaceholder) {
		return naPlaceholder
	}
	return value
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func humanizeState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return "yes"
	case "false":
		return "no"
	default:
		return displayValue(value)
	}
}
