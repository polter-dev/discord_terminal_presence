package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

var errCommandUsage = errors.New("invalid command usage")

type connectCommandDeps struct {
	now          func() time.Time
	readState    func(string) (daemonDiscordState, bool)
	readFresh    func(string, time.Time, time.Duration) (daemonDiscordState, bool)
	readPID      func(string) (int, error)
	readPIDStart func(string, int) uint64
	alive        func(int) bool
	looksLike    func(int) bool
	send         func(context.Context, int, controlRequest) (controlResponse, error)
	sleep        func(time.Duration)
	pidPath      string
	discordPath  string
	timeout      time.Duration
	pollInterval time.Duration
	firstRunCTA  func()
}

func defaultConnectCommandDeps() connectCommandDeps {
	return connectCommandDeps{
		now:          time.Now,
		readState:    readDaemonDiscordState,
		readFresh:    readFreshDaemonDiscordState,
		readPID:      readPID,
		readPIDStart: readPIDStartTime,
		alive:        processAlive,
		looksLike:    processLooksLikeTermp,
		send:         sendControlRequest,
		sleep:        time.Sleep,
		pidPath:      pidFilePath(),
		discordPath:  daemonDiscordStatePath(),
		timeout:      controlRequestTimeout,
		pollInterval: controlPollInterval,
		firstRunCTA: func() {
			maybePrintFirstRunCTA(os.Stdout, config.DefaultPath(), isTerminal(os.Stdout))
		},
	}
}

func connectCommand(args []string) error {
	return connectCommandWith(args, os.Stdout, os.Stderr, defaultConnectCommandDeps())
}

func connectCommandWith(args []string, output, errorOutput io.Writer, deps connectCommandDeps) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(errorOutput)
	force := fs.Bool("force", false, "reconnect even when already connected")
	addVerboseFlag(fs)
	fs.Usage = func() {
		fmt.Fprintln(errorOutput, "usage: termp connect [--force]")
		fmt.Fprintln(errorOutput, "  --force       reconnect even when already connected")
		fmt.Fprintln(errorOutput, "  -v, --verbose enable verbose logging")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %v", errCommandUsage, err)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("%w: unexpected argument %q", errCommandUsage, fs.Arg(0))
	}
	if deps.firstRunCTA != nil {
		deps.firstRunCTA()
	}

	now := deps.now()
	targetPID := 0
	if state, ok := deps.readFresh(deps.discordPath, now, daemonDiscordStateStaleAfter); ok &&
		processIdentityMatches(state.PID, state.StartTime, deps.alive, deps.looksLike) {
		targetPID = state.PID
	}
	if targetPID == 0 {
		if pid, err := deps.readPID(deps.pidPath); err == nil &&
			processIdentityMatches(pid, deps.readPIDStart(deps.pidPath, pid), deps.alive, deps.looksLike) {
			targetPID = pid
		}
	}
	if targetPID == 0 {
		return errors.New(`daemon is not running; run "termp start" first`)
	}

	baseline, _ := deps.readState(deps.discordPath)
	ctx, cancel := context.WithTimeout(context.Background(), deps.timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	response, err := deps.send(ctx, targetPID, controlRequest{Command: "connect", Force: *force})
	if err != nil {
		return fmt.Errorf("re-establish Discord connection through daemon pid %d: %w", targetPID, err)
	}
	switch response.Status {
	case "already_connected":
		fmt.Fprintf(output, "already connected (pid %d)\n", targetPID)
		return nil
	case "connected":
	default:
		return fmt.Errorf("daemon pid %d returned unknown control status %q", targetPID, response.Status)
	}

	if err := waitForDaemonConnection(
		deps.discordPath,
		targetPID,
		baseline.UpdatedAt,
		time.Until(deadline),
		deps.pollInterval,
		deps.readState,
		deps.alive,
		deps.sleep,
	); err != nil {
		return err
	}
	fmt.Fprintf(output, "connected (pid %d)\n", targetPID)
	return nil
}

func waitForDaemonConnection(
	path string,
	targetPID int,
	after time.Time,
	timeout, pollInterval time.Duration,
	read func(string) (daemonDiscordState, bool),
	alive func(int) bool,
	sleep func(time.Duration),
) error {
	for waited := time.Duration(0); ; {
		state, ok := read(path)
		if ok && state.PID == targetPID && state.Connected && state.UpdatedAt.After(after) {
			return nil
		}
		if !alive(targetPID) {
			return fmt.Errorf("daemon pid %d exited before the Discord connection was confirmed", targetPID)
		}
		if timeout <= 0 || pollInterval <= 0 || waited >= timeout {
			return fmt.Errorf("Discord connection could not be confirmed within %s for daemon pid %d", timeout, targetPID)
		}
		delay := min(pollInterval, timeout-waited)
		sleep(delay)
		waited += delay
	}
}

type discordReconnector interface {
	Reconnect(context.Context, bool) (bool, error)
}

type daemonControl struct {
	ready       chan struct{}
	reconnector discordReconnector
}

func newDaemonControl() *daemonControl {
	return &daemonControl{ready: make(chan struct{})}
}

func (control *daemonControl) setReconnector(reconnector discordReconnector) {
	control.reconnector = reconnector
	close(control.ready)
}

func (control *daemonControl) handle(ctx context.Context, request controlRequest) controlResponse {
	select {
	case <-control.ready:
		return daemonControlHandler(control.reconnector)(ctx, request)
	case <-ctx.Done():
		return controlResponse{Error: fmt.Sprintf("daemon control is not ready: %v", ctx.Err())}
	}
}

func daemonControlHandler(reconnector discordReconnector) controlHandler {
	return func(ctx context.Context, request controlRequest) controlResponse {
		if request.Command != "connect" {
			return controlResponse{Error: fmt.Sprintf("unsupported daemon control command %q", request.Command)}
		}
		alreadyConnected, err := reconnector.Reconnect(ctx, request.Force)
		if err != nil {
			return controlResponse{Error: fmt.Sprintf("Discord connection failed: %v", err)}
		}
		if alreadyConnected {
			return controlResponse{Status: "already_connected"}
		}
		return controlResponse{Status: "connected"}
	}
}
