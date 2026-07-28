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

func displayValue(value string) string {
	value = strings.TrimSpace(terminaltext.SanitizeSingleLine(value))
	if value == "" || strings.EqualFold(value, "n/a") {
		return "—"
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
