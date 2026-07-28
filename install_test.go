package termp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerFailuresPrintRetryCommandAndLeaveNoBinary(t *testing.T) {
	tests := []struct {
		name       string
		curlScript string
		tarScript  string
	}{
		{
			name: "checksum download",
			curlScript: `case "$url" in
	*/checksums.txt) exit 22 ;;
	*) printf 'partial archive' >"$dest" ;;
esac`,
		},
		{
			name: "fallback archive download",
			curlScript: `case "$url" in
	https://termp.polter.sh/*) exit 22 ;;
	*/checksums.txt) printf '%064d  termp_1.2.3_linux_amd64.tar.gz\n' 0 >"$dest" ;;
	*) exit 22 ;;
esac`,
		},
		{
			name: "archive extraction",
			curlScript: `case "$url" in
	*/checksums.txt) printf '%064d  termp_1.2.3_linux_amd64.tar.gz\n' 0 >"$dest" ;;
	*) printf 'archive' >"$dest" ;;
esac`,
			tarScript: "exit 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "install bin")
			if err := os.Mkdir(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			fakeBin := filepath.Join(root, "fakebin")
			if err := os.Mkdir(fakeBin, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, fakeBin, "uname", `#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) exit 1 ;;
esac
`)
			writeExecutable(t, fakeBin, "curl", `#!/bin/sh
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o) dest=$2; shift 2 ;;
		*) url=$1; shift ;;
	esac
done
`+tt.curlScript+`
`)
			writeExecutable(t, fakeBin, "sha256sum", `#!/bin/sh
printf '%064d  %s\n' 0 "$1"
`)
			tarScript := tt.tarScript
			if tarScript == "" {
				tarScript = "exit 99"
			}
			writeExecutable(t, fakeBin, "tar", "#!/bin/sh\n"+tarScript+"\n")

			command := exec.Command("sh", "install.sh")
			command.Env = append(os.Environ(),
				"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"VERSION=v1.2.3",
				"BINDIR="+binDir,
				"TERMP_DOWNLOAD_CHANNEL=update",
				"NO_UPDATE_CHECK=1",
				"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
				"XDG_STATE_HOME="+filepath.Join(root, "state"),
				"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("installer succeeded unexpectedly:\n%s", output)
			}

			wantRetry := "termp install: retry with: curl -fsSL https://termp.polter.sh/install.sh | " +
				"VERSION='v1.2.3' BINDIR='" + binDir + "' TERMP_DOWNLOAD_CHANNEL='update' sh"
			if got := string(output); !strings.Contains(got, wantRetry) {
				t.Fatalf("installer output missing retry command %q:\n%s", wantRetry, got)
			}
			if strings.Contains(string(output), " sh install.sh") {
				t.Fatalf("retry guidance refers to a local install.sh:\n%s", output)
			}
			if _, statErr := os.Stat(filepath.Join(binDir, "termp")); !os.IsNotExist(statErr) {
				t.Fatalf("failed install left destination binary behind: %v", statErr)
			}
		})
	}
}

func TestInstallerLabelsUninstallAsLoginOnly(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	if strings.Contains(text, "remove:   termp uninstall") {
		t.Fatal("installer still labels the start-at-login alias as full removal")
	}
	if !strings.Contains(text, "login off: termp uninstall") {
		t.Fatal("installer missing login-only guidance")
	}
}

func writeExecutable(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}
