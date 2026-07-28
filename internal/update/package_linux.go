//go:build linux

package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func performSystemPackageUpdate(
	ctx context.Context,
	method InstallMethod,
	tag string,
	runner CommandRunner,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	if !validReleaseTag(tag) {
		return fmt.Errorf("invalid release tag %q", tag)
	}
	if method == InstallSystemPackage {
		return errors.New("cannot determine whether the system package is Debian or RPM")
	}

	var extension, manager string
	switch method {
	case InstallDebian:
		extension, manager = ".deb", "apt"
	case InstallRPM:
		extension, manager = ".rpm", "dnf"
	default:
		return fmt.Errorf("unsupported system package method %q", method)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("unsupported update architecture %q", runtime.GOARCH)
	}
	if isExecRunner(runner) {
		if !readerIsTerminal(stdin) {
			return errors.New("interactive terminal required for sudo")
		}
		if _, err := exec.LookPath("sudo"); err != nil {
			return errors.New("sudo is not available")
		}
		if _, err := exec.LookPath(manager); err != nil {
			return fmt.Errorf("%s is not available", manager)
		}
	}

	version := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	assetName := fmt.Sprintf("termp_%s_linux_%s%s", version, runtime.GOARCH, extension)
	asset, err := os.CreateTemp("", "termp-package-*"+extension)
	if err != nil {
		return fmt.Errorf("create temporary package: %w", err)
	}
	assetPath := asset.Name()
	defer removePackageTemporaryFile(assetPath)
	if err := asset.Close(); err != nil {
		return fmt.Errorf("create temporary package: %w", err)
	}

	checksums, err := os.CreateTemp("", "termp-checksums-*.txt")
	if err != nil {
		return fmt.Errorf("create temporary checksums: %w", err)
	}
	checksumsPath := checksums.Name()
	defer removePackageTemporaryFile(checksumsPath)
	if err := checksums.Close(); err != nil {
		return fmt.Errorf("create temporary checksums: %w", err)
	}

	baseURL := fmt.Sprintf("https://github.com/polter-dev/discord_terminal_presence/releases/download/%s", tag)
	assetURL := baseURL + "/" + assetName
	if err := runner.Run(ctx, Command{
		Name: "curl",
		Args: []string{"-fsSL", assetURL, "-o", assetPath},
	}, nil, stdout, stderr); err != nil {
		return fmt.Errorf("download %s: %w", assetName, err)
	}
	if err := runner.Run(ctx, Command{
		Name: "curl",
		Args: []string{"-fsSL", baseURL + "/checksums.txt", "-o", checksumsPath},
	}, nil, stdout, stderr); err != nil {
		return fmt.Errorf("download checksums for %s: %w", tag, err)
	}
	if err := verifyPackageChecksum(checksumsPath, assetPath, assetName); err != nil {
		return err
	}

	command := Command{Name: "sudo", Args: []string{manager, "install", "-y", assetPath}}
	if err := runner.Run(ctx, command, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("install %s with %s: %w", assetName, manager, err)
	}
	return nil
}

func isExecRunner(runner CommandRunner) bool {
	switch runner.(type) {
	case ExecRunner, *ExecRunner:
		return true
	default:
		return false
	}
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func removePackageTemporaryFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("termp update: remove temporary package file %s: %v", path, err)
	}
}

func verifyPackageChecksum(checksumsPath, assetPath, assetName string) error {
	info, err := os.Stat(assetPath)
	if err != nil {
		return fmt.Errorf("inspect downloaded %s: %w", assetName, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("downloaded %s is empty", assetName)
	}

	checksums, err := os.Open(checksumsPath)
	if err != nil {
		return fmt.Errorf("open checksums for %s: %w", assetName, err)
	}
	defer checksums.Close()

	var expected string
	matches := 0
	scanner := bufio.NewScanner(checksums)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == assetName {
			expected = fields[0]
			matches++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read checksums for %s: %w", assetName, err)
	}
	if matches != 1 {
		return fmt.Errorf("expected exactly one checksum for %s, found %d", assetName, matches)
	}
	decoded, err := hex.DecodeString(expected)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid SHA-256 checksum for %s", assetName)
	}

	asset, err := os.Open(assetPath)
	if err != nil {
		return fmt.Errorf("open downloaded %s: %w", assetName, err)
	}
	defer asset.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, asset); err != nil {
		return fmt.Errorf("calculate SHA-256 checksum for %s: %w", assetName, err)
	}
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expected) {
		return fmt.Errorf("checksum mismatch for %s", assetName)
	}
	return nil
}
