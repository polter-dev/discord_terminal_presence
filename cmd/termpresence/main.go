package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("termpresence: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "start":
		err = start(os.Args[2:])
	case "stop":
		err = stop(os.Args[2:])
	case "status":
		err = status(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: termpresence start|stop|status")
}

func start(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	pidPath := pidFilePath()
	if pid, err := readPID(pidPath); err == nil && processAlive(pid) {
		return fmt.Errorf("daemon already running with pid %d", pid)
	}
	if err := writePID(pidPath, os.Getpid()); err != nil {
		return err
	}
	defer removePID(pidPath)

	manager := config.NewManager()
	cfg, loadErr := manager.Current()
	if loadErr != nil {
		log.Printf("config load error, using last-good/default config: %v", loadErr)
	}
	for _, warning := range cfg.Warnings {
		log.Print(warning)
	}

	if err := config.EnsureConfigDir(cfg.Path); err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := manager.Watch(ctx); err != nil {
			log.Printf("config watch disabled: %v", err)
		}
		return run(ctx, manager)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return run(ctx, manager)
}

func run(ctx context.Context, manager *config.Manager) error {
	cfg, _ := manager.Current()
	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return err
	}
	det, err := detector.New(reg, detector.GopsutilLister{}, detector.Config{
		ScanInterval: cfg.ScanIntervalDuration(),
	})
	if err != nil {
		return err
	}

	detections := det.Run(ctx)
	for {
		select {
		case detection, ok := <-detections:
			if !ok {
				return nil
			}
			if detection.None {
				log.Print("no known terminal tool detected")
				continue
			}
			log.Printf("detected %s cwd=%s", detection.Tool.ID, detection.Cwd)
			// TODO(M2 integration): send detections to internal/presence.Writer once that package merges.
		case <-manager.Changes():
			log.Print("config reloaded")
			// TODO(M2 integration): rebuild registry/detector or pass config changes to presence writer.
		case <-ctx.Done():
			return nil
		}
	}
}

func stop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	pidPath := pidFilePath()
	pid, err := readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("daemon is not running")
		}
		return err
	}
	if !processAlive(pid) {
		removePID(pidPath)
		return errors.New("stale PID file removed; daemon is not running")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	removePID(pidPath)
	fmt.Println("stopped")
	return nil
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, loadErr := config.Load()
	pidPath := pidFilePath()
	running := false
	if pid, err := readPID(pidPath); err == nil {
		running = processAlive(pid)
	}

	fmt.Printf("running: %t\n", running)
	fmt.Printf("config_path: %s\n", cfg.Path)
	if loadErr != nil {
		fmt.Printf("config_ok: false\nconfig_error: %v\n", loadErr)
	} else {
		fmt.Println("config_ok: true")
		for _, warning := range cfg.Warnings {
			fmt.Printf("config_warning: %s\n", warning)
		}
	}

	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		return err
	}
	processes, err := detector.GopsutilLister{}.List()
	if err != nil {
		fmt.Printf("detected_tool: unknown (%v)\n", err)
		return nil
	}
	detection := detector.ActiveDetection(reg, processes)
	if detection.None {
		fmt.Println("detected_tool: none")
		return nil
	}
	fmt.Printf("detected_tool: %s\n", detection.Tool.ID)
	return nil
}

func pidFilePath() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "termpresence.pid")
	}
	return filepath.Join(os.TempDir(), "termpresence.pid")
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func writePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func removePID(path string) {
	_ = os.Remove(path)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
