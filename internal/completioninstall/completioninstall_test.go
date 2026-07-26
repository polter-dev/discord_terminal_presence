package completioninstall

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstallAndUninstall(t *testing.T) {
	tests := []struct {
		shell    string
		relative string
	}{
		{shell: "bash", relative: filepath.Join(".local", "share", "bash-completion", "completions", "termp")},
		{shell: "zsh", relative: filepath.Join(".zsh", "completions", "_termp")},
		{shell: "fish", relative: filepath.Join(".config", "fish", "completions", "termp.fish")},
	}

	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			home := t.TempDir()
			resolveHome := func() (string, error) { return home, nil }
			wantPath := filepath.Join(home, tt.relative)
			firstScript := "# first completion\ncomplete termp\n"
			secondScript := "# replacement completion\ncomplete termp\n"

			if path, err := TargetPath(tt.shell, resolveHome); err != nil || path != wantPath {
				t.Fatalf("TargetPath() = %q, %v; want %q", path, err, wantPath)
			}
			paths, err := Install(tt.shell, firstScript, resolveHome)
			if err != nil {
				t.Fatalf("Install() error = %v", err)
			}
			if !reflect.DeepEqual(paths, []string{wantPath}) {
				t.Fatalf("Install() paths = %#v, want [%q]", paths, wantPath)
			}
			assertFileContents(t, wantPath, firstScript)

			paths, err = Install(tt.shell, secondScript, resolveHome)
			if err != nil {
				t.Fatalf("second Install() error = %v", err)
			}
			if !reflect.DeepEqual(paths, []string{wantPath}) {
				t.Fatalf("second Install() paths = %#v, want [%q]", paths, wantPath)
			}
			assertFileContents(t, wantPath, secondScript)

			paths, err = Uninstall(tt.shell, resolveHome)
			if err != nil {
				t.Fatalf("Uninstall() error = %v", err)
			}
			if !reflect.DeepEqual(paths, []string{wantPath}) {
				t.Fatalf("Uninstall() paths = %#v, want [%q]", paths, wantPath)
			}
			if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
				t.Fatalf("completion file still exists after uninstall: %v", err)
			}

			paths, err = Uninstall(tt.shell, resolveHome)
			if err != nil {
				t.Fatalf("idempotent Uninstall() error = %v", err)
			}
			if len(paths) != 0 {
				t.Fatalf("idempotent Uninstall() paths = %#v, want none", paths)
			}
		})
	}
}

func TestDetectShell(t *testing.T) {
	for input, want := range map[string]string{
		"/bin/bash":              "bash",
		"/usr/local/bin/zsh":     "zsh",
		"/opt/homebrew/bin/fish": "fish",
		"":                       "bash",
		"/bin/unknown":           "bash",
	} {
		if got := DetectShell(input); got != want {
			t.Errorf("DetectShell(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestZshNoteDoesNotModifyRCFile(t *testing.T) {
	if got := Note("zsh"); got != "If needed, add `fpath=(~/.zsh/completions $fpath)` to ~/.zshrc before compinit." {
		t.Fatalf("Note(zsh) = %q", got)
	}
	if got := Note("bash"); got != "" {
		t.Fatalf("Note(bash) = %q, want empty", got)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
}
