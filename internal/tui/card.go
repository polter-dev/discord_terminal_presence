package tui

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
)

// CardState is everything the mock Discord card needs to render one frame.
type CardState struct {
	Activity  *presence.Activity
	Connected bool
	Now       time.Time
	Recent    []RecentDetection
}

// RecentDetection is one recently featured tool.
type RecentDetection struct {
	Name string
	At   time.Time
}

// CardStyles contains the small style surface used by RenderCard.
type CardStyles struct {
	Title  lipgloss.Style
	Muted  lipgloss.Style
	Accent lipgloss.Style
	Card   lipgloss.Style
}

// DefaultCardStyles returns the shared preview styles.
func DefaultCardStyles() CardStyles {
	return CardStyles{
		Title:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")),
		Muted:  lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Accent: lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
		Card:   lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2),
	}
}

// RenderCard returns the full styled multi-line card string. It performs no I/O
// and reads no clock; callers inject Now for deterministic rendering.
func RenderCard(s CardState, st CardStyles) string {
	if s.Now.IsZero() {
		s.Now = time.Unix(0, 0)
	}

	var body strings.Builder
	status := "not running"
	if s.Connected {
		status = "connected"
	}
	body.WriteString(st.Muted.Render("discord: " + status))
	body.WriteByte('\n')
	body.WriteByte('\n')

	if s.Activity == nil {
		body.WriteString(st.Title.Render("idle - no tool detected"))
	} else {
		writeActivity(&body, s.Activity, s.Now, st)
	}

	if len(s.Recent) > 0 {
		body.WriteString("\n\n")
		body.WriteString(st.Muted.Render("recent detections"))
		for _, recent := range s.Recent {
			if recent.Name == "" {
				continue
			}
			body.WriteByte('\n')
			body.WriteString(fmt.Sprintf("%s  %s", recent.Name, relativeTime(s.Now, recent.At)))
		}
	}

	return st.Card.Render(body.String())
}

func writeActivity(b *strings.Builder, activity *presence.Activity, now time.Time, st CardStyles) {
	name := activity.LargeImage.Text
	if name == "" {
		name = "unknown tool"
	}
	b.WriteString(st.Title.Render(name))
	image := imageLabel(activity.LargeImage, name)
	if image != "" {
		b.WriteByte('\n')
		b.WriteString(st.Muted.Render(image))
	}
	if activity.State != "" {
		b.WriteByte('\n')
		b.WriteString(activity.State)
	}
	if activity.Details != "" {
		b.WriteByte('\n')
		b.WriteString(activity.Details)
	}
	if activity.StartTimestamp != nil {
		if elapsed := elapsedString(now, *activity.StartTimestamp); elapsed != "" {
			b.WriteByte('\n')
			b.WriteString("elapsed: " + elapsed)
		}
	}
	if image := imageLabel(activity.SmallImage, ""); image != "" {
		b.WriteByte('\n')
		b.WriteString("small image: " + image)
	}
	if len(activity.Buttons) > 0 {
		labels := make([]string, 0, len(activity.Buttons))
		for _, button := range activity.Buttons {
			if button.Label != "" {
				labels = append(labels, button.Label)
			}
		}
		if len(labels) > 0 {
			b.WriteByte('\n')
			b.WriteString("buttons: " + strings.Join(labels, " | "))
		}
	}
}

func imageLabel(image presence.Image, cardTitle string) string {
	if strings.TrimSpace(image.Key) == "" && strings.TrimSpace(image.URL) == "" {
		return ""
	}

	names := []string{image.Text, image.Key, imageNameFromURL(image.URL)}
	if cardTitle != "" {
		names = []string{image.Key, imageNameFromURL(image.URL), image.Text}
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && name != strings.TrimSpace(cardTitle) {
			return "[image: " + name + "]"
		}
	}
	return "[image]"
}

func imageNameFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	if name == "" || name == "." || name == "/" {
		return ""
	}
	return strings.TrimSuffix(name, path.Ext(name))
}

func elapsedString(now, started time.Time) string {
	if started.IsZero() || now.Before(started) {
		return ""
	}
	d := now.Sub(started).Truncate(time.Second)
	hours := int(d / time.Hour)
	minutes := int(d/time.Minute) % 60
	seconds := int(d/time.Second) % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func relativeTime(now, at time.Time) string {
	if at.IsZero() || now.Before(at) {
		return "just now"
	}
	d := now.Sub(at).Truncate(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	default:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
}
