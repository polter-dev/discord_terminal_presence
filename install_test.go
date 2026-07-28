package termp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerFailuresPrintRetryCommandAndLeaveNoBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is not used on Windows")
	}

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
			curlLog := filepath.Join(root, "curl.log")
			writeExecutable(t, fakeBin, "uname", `#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) exit 1 ;;
esac
`)
			writeExecutable(t, fakeBin, "curl", `#!/bin/sh
printf '%s\n' "$*" >>"$CURL_LOG"
while [ "$#" -gt 0 ]; do
	case "$1" in
		--connect-timeout | --max-time) shift 2 ;;
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
				"CURL_LOG="+curlLog,
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
			curlCalls, readErr := os.ReadFile(curlLog)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, call := range strings.Split(strings.TrimSpace(string(curlCalls)), "\n") {
				if !strings.Contains(call, "--connect-timeout 10 --max-time 300 -fsSL") {
					t.Fatalf("installer curl call has no bounded transfer: %q", call)
				}
			}
		})
	}
}

func TestInstallerWgetDownloadsAreBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is not used on Windows")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "install bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(root, "fakebin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"awk", "mkdir", "mktemp", "rm", "sed"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("find test dependency %s: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(fakeBin, name)); err != nil {
			t.Fatalf("link test dependency %s: %v", name, err)
		}
	}

	wgetLog := filepath.Join(root, "wget.log")
	writeExecutable(t, fakeBin, "uname", `#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) exit 1 ;;
esac
`)
	writeExecutable(t, fakeBin, "wget", `#!/bin/sh
printf '%s\n' "$*" >>"$WGET_LOG"
while [ "$#" -gt 0 ]; do
	case "$1" in
		--timeout=10 | --tries=1 | -q) shift ;;
		-O) dest=$2; shift 2 ;;
		*) url=$1; shift ;;
	esac
done
case "$url" in
	*/checksums.txt) printf '%064d  termp_1.2.3_linux_amd64.tar.gz\n' 0 >"$dest" ;;
	*) printf 'archive' >"$dest" ;;
esac
`)
	writeExecutable(t, fakeBin, "sha256sum", `#!/bin/sh
printf '%064d  %s\n' 0 "$1"
`)
	writeExecutable(t, fakeBin, "tar", "#!/bin/sh\nexit 2\n")

	command := exec.Command("sh", "install.sh")
	command.Env = append(os.Environ(),
		"PATH="+fakeBin,
		"VERSION=v1.2.3",
		"BINDIR="+binDir,
		"TERMP_DOWNLOAD_CHANNEL=update",
		"NO_UPDATE_CHECK=1",
		"WGET_LOG="+wgetLog,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("installer succeeded unexpectedly:\n%s", output)
	}

	wgetCalls, readErr := os.ReadFile(wgetLog)
	if readErr != nil {
		t.Fatalf("read wget log: %v\ninstaller output:\n%s", readErr, output)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(wgetCalls)), "\n") {
		if !strings.Contains(call, "--timeout=10 --tries=1 -q") {
			t.Fatalf("installer wget call has no bounded retry/transfer: %q", call)
		}
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
