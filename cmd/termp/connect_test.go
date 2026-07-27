package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func baseConnectDeps(now time.Time) connectCommandDeps {
	return connectCommandDeps{
		now:       func() time.Time { return now },
		readState: func(string) (daemonDiscordState, bool) { return daemonDiscordState{}, false },
		readFresh: func(string, time.Time, time.Duration) (daemonDiscordState, bool) { return daemonDiscordState{}, false },
		readPID:   func(string) (daemonPIDRecord, error) { return daemonPIDRecord{}, errors.New("missing") },
		alive:     func(int) bool { return false },
		looksLike: func(int) bool { return false },
		send: func(context.Context, int, controlRequest) (controlResponse, error) {
			return controlResponse{}, errors.New("unexpected send")
		},
		sleep:        func(time.Duration) {},
		pidPath:      "termp.pid",
		discordPath:  "discord.json",
		timeout:      time.Second,
		pollInterval: 25 * time.Millisecond,
	}
}

func TestConnectCommandFailsWithStartInstructionWhenNoDaemonRuns(t *testing.T) {
	var output bytes.Buffer
	err := connectCommandWith(nil, &output, &output, baseConnectDeps(time.Now()))
	if err == nil || !strings.Contains(err.Error(), `run "termp start"`) {
		t.Fatalf("connect error = %v, want start instruction", err)
	}
	if output.Len() != 0 {
		t.Fatalf("connect output = %q, want none", output.String())
	}
}

func TestConnectCommandPrintsFirstRunCTAAfterValidArguments(t *testing.T) {
	deps := baseConnectDeps(time.Now())
	ctas := 0
	deps.firstRunCTA = func() { ctas++ }
	var output bytes.Buffer

	_ = connectCommandWith(nil, &output, &output, deps)
	if ctas != 1 {
		t.Fatalf("valid connect CTA calls = %d, want 1", ctas)
	}

	ctas = 0
	_ = connectCommandWith([]string{"production"}, &output, &output, deps)
	if ctas != 0 {
		t.Fatalf("invalid connect CTA calls = %d, want 0", ctas)
	}
}

func TestConnectCommandTargetsPublisherAndWaitsForNewConnectedState(t *testing.T) {
	useFixtureProcessStartTime(t)
	now := time.Date(2026, 7, 25, 20, 0, 0, 0, time.UTC)
	deps := baseConnectDeps(now)
	deps.readFresh = func(string, time.Time, time.Duration) (daemonDiscordState, bool) {
		return daemonDiscordState{PID: 22, StartTime: fixtureProcessStartTime, UpdatedAt: now}, true
	}
	deps.readPID = func(string) (daemonPIDRecord, error) {
		return daemonPIDRecord{PID: 11, StartTime: fixtureProcessStartTime}, nil
	}
	deps.alive = func(pid int) bool { return pid == 11 || pid == 22 }
	deps.looksLike = deps.alive
	states := []daemonDiscordState{
		{PID: 22, StartTime: fixtureProcessStartTime, Connected: false, UpdatedAt: now},
		{PID: 22, StartTime: fixtureProcessStartTime, Connected: false, UpdatedAt: now},
		{PID: 22, StartTime: fixtureProcessStartTime, Connected: true, UpdatedAt: now.Add(time.Millisecond)},
	}
	deps.readState = func(string) (daemonDiscordState, bool) {
		state := states[0]
		if len(states) > 1 {
			states = states[1:]
		}
		return state, true
	}
	var target int
	deps.send = func(_ context.Context, pid int, request controlRequest) (controlResponse, error) {
		target = pid
		if request.Command != "connect" || request.Force {
			t.Fatalf("request = %+v, want ordinary connect", request)
		}
		return controlResponse{Status: "connected"}, nil
	}

	var output bytes.Buffer
	if err := connectCommandWith(nil, &output, &output, deps); err != nil {
		t.Fatal(err)
	}
	if target != 22 {
		t.Fatalf("target pid = %d, want publisher pid 22", target)
	}
	if got := output.String(); got != "connected (pid 22)\n" {
		t.Fatalf("connect output = %q", got)
	}
}

func TestConnectCommandSurfacesReconnectFailureWithoutSuccess(t *testing.T) {
	useFixtureProcessStartTime(t)
	now := time.Now()
	deps := baseConnectDeps(now)
	deps.readPID = func(string) (daemonPIDRecord, error) {
		return daemonPIDRecord{PID: 42, StartTime: fixtureProcessStartTime}, nil
	}
	deps.alive = func(pid int) bool { return pid == 42 }
	deps.looksLike = deps.alive
	deps.send = func(context.Context, int, controlRequest) (controlResponse, error) {
		return controlResponse{}, errors.New("Discord IPC endpoint not found")
	}

	var output bytes.Buffer
	err := connectCommandWith(nil, &output, &output, deps)
	if err == nil || !strings.Contains(err.Error(), "Discord IPC endpoint not found") {
		t.Fatalf("connect error = %v, want real reconnect failure", err)
	}
	if strings.Contains(output.String(), "connected") {
		t.Fatalf("failure output claims success: %q", output.String())
	}
}

func TestConnectCommandTimeoutDoesNotReportSuccess(t *testing.T) {
	useFixtureProcessStartTime(t)
	now := time.Now()
	deps := baseConnectDeps(now)
	deps.timeout = 50 * time.Millisecond
	deps.pollInterval = 25 * time.Millisecond
	deps.readPID = func(string) (daemonPIDRecord, error) {
		return daemonPIDRecord{PID: 42, StartTime: fixtureProcessStartTime}, nil
	}
	deps.alive = func(pid int) bool { return pid == 42 }
	deps.looksLike = deps.alive
	deps.readState = func(string) (daemonDiscordState, bool) {
		return daemonDiscordState{PID: 42, Connected: false, UpdatedAt: now}, true
	}
	deps.send = func(context.Context, int, controlRequest) (controlResponse, error) {
		return controlResponse{Status: "connected"}, nil
	}

	var output bytes.Buffer
	err := connectCommandWith(nil, &output, &output, deps)
	if err == nil || !strings.Contains(err.Error(), "could not be confirmed") {
		t.Fatalf("connect error = %v, want readiness timeout", err)
	}
	if strings.Contains(output.String(), "connected") {
		t.Fatalf("timeout output claims success: %q", output.String())
	}
}

func TestConnectCommandReportsAlreadyConnected(t *testing.T) {
	useFixtureProcessStartTime(t)
	now := time.Now()
	deps := baseConnectDeps(now)
	deps.readPID = func(string) (daemonPIDRecord, error) {
		return daemonPIDRecord{PID: 42, StartTime: fixtureProcessStartTime}, nil
	}
	deps.alive = func(pid int) bool { return pid == 42 }
	deps.looksLike = deps.alive
	deps.send = func(_ context.Context, _ int, request controlRequest) (controlResponse, error) {
		if request.Force {
			t.Fatal("ordinary connect unexpectedly forced reconnect")
		}
		return controlResponse{Status: "already_connected"}, nil
	}

	var output bytes.Buffer
	if err := connectCommandWith(nil, &output, &output, deps); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "already connected (pid 42)\n" {
		t.Fatalf("connect output = %q", got)
	}
}

type fakeReconnector struct {
	force bool
	err   error
}

func (f *fakeReconnector) Reconnect(_ context.Context, force bool) (bool, error) {
	f.force = force
	return false, f.err
}

func TestDaemonControlHandlerUsesInjectedReconnector(t *testing.T) {
	reconnector := &fakeReconnector{err: errors.New("handshake failed")}
	response := daemonControlHandler(reconnector)(context.Background(), controlRequest{Command: "connect", Force: true})
	if !reconnector.force || !strings.Contains(response.Error, "handshake failed") {
		t.Fatalf("handler response = %+v, force = %t", response, reconnector.force)
	}
}

func TestControlProtocolRoundTripWithInjectedConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go serveControlConnection(context.Background(), server, func(_ context.Context, request controlRequest) controlResponse {
		if request.Command != "connect" || !request.Force {
			t.Errorf("request = %+v", request)
		}
		return controlResponse{Status: "connected"}
	})
	response, err := exchangeControlRequest(context.Background(), client, controlRequest{Command: "connect", Force: true})
	if err != nil || response.Status != "connected" {
		t.Fatalf("control response = %+v, %v", response, err)
	}
}
