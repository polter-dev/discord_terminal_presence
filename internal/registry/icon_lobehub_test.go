package registry

import (
	"net/url"
	"strings"
	"testing"
)

// TestResolveIconLobeHubEscapesHostileSlugs pins #575: the LobeHub icon
// branch used to concatenate an unescaped slug into a URL path segment,
// unlike the sibling Simple Icons branch (#489), which let a slug leave the
// intended package path, add a query or fragment, or contain a raw space,
// all while still passing ValidateHTTPURL (host/scheme only). These are the
// exact hostile slugs from the #575 reproduction.
func TestResolveIconLobeHubEscapesHostileSlugs(t *testing.T) {
	tests := []struct {
		name string
		slug string
	}{
		{name: "path traversal out of the package path", slug: "../../../../@evil/pkg@1.0.0/payload"},
		{name: "injected query parameter", slug: "git?x="},
		{name: "injected fragment", slug: "git#frag"},
		{name: "raw space", slug: "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &Tool{IconSlug: tt.slug, IconSource: IconSourceLobeHub}
			resolveIcon(tool)

			parsed, err := url.Parse(tool.ImageURL)
			if err != nil {
				t.Fatalf("resolved URL %q does not parse: %v", tool.ImageURL, err)
			}
			if parsed.Host != "unpkg.com" {
				t.Fatalf("resolved URL host = %q, want unpkg.com (slug=%q url=%q)", parsed.Host, tt.slug, tool.ImageURL)
			}
			// The *decoded* Path is not the right thing to check here: a
			// properly escaped "/" decodes right back to "/" in
			// parsed.Path, so a prefix or segment-count check against Path
			// cannot tell an escaped slash from a literal one. EscapedPath
			// is the wire representation an HTTP client actually sends,
			// which is what has to stay inside the intended package path.
			const wantPrefix = "/@lobehub/icons-static-png@1.91.0/dark/"
			escapedPath := parsed.EscapedPath()
			if !strings.HasPrefix(escapedPath, wantPrefix) {
				t.Fatalf("resolved URL escaped path = %q escaped out of the intended package path (slug=%q url=%q)", escapedPath, tt.slug, tool.ImageURL)
			}
			remainder := strings.TrimPrefix(escapedPath, wantPrefix)
			if strings.Contains(remainder, "/") {
				t.Fatalf("resolved URL escaped path %q contains an extra path segment after escaping (slug=%q url=%q)", escapedPath, tt.slug, tool.ImageURL)
			}
			if parsed.RawQuery != "" {
				t.Fatalf("resolved URL has a query %q, want none (slug=%q url=%q)", parsed.RawQuery, tt.slug, tool.ImageURL)
			}
			if parsed.Fragment != "" {
				t.Fatalf("resolved URL has a fragment %q, want none (slug=%q url=%q)", parsed.Fragment, tt.slug, tool.ImageURL)
			}
			if strings.ContainsAny(tool.ImageURL, " \t\n") {
				t.Fatalf("resolved URL %q contains a raw whitespace character (slug=%q)", tool.ImageURL, tt.slug)
			}
			if err := ValidateHTTPURL(tool.ImageURL); err != nil {
				t.Fatalf("ValidateHTTPURL(%q) = %v, want the escaped URL to still validate", tool.ImageURL, err)
			}
		})
	}
}

// TestResolveIconLobeHubPlainSlugUnchanged proves the escaping fix does not
// disturb an ordinary slug: it must still resolve to the same working proxy
// URL it did before #575.
func TestResolveIconLobeHubPlainSlugUnchanged(t *testing.T) {
	tool := &Tool{IconSlug: "git", IconSource: IconSourceLobeHub}
	resolveIcon(tool)
	want := "https://unpkg.com/@lobehub/icons-static-png@1.91.0/dark/git.png"
	if tool.ImageURL != want {
		t.Fatalf("resolveIcon ImageURL = %q, want %q", tool.ImageURL, want)
	}
}
