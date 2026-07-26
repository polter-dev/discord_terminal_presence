//go:build windows

package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func controlPipeName(pid int) string {
	return `\\.\pipe\termp-control-` + strconv.Itoa(pid)
}

func startControlServer(ctx context.Context, pid int, handler controlHandler) (func(), error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("resolve control pipe owner: %w", err)
	}
	listener, err := winio.ListenPipe(controlPipeName(pid), &winio.PipeConfig{
		SecurityDescriptor: "O:" + sid.String() + "D:P(A;;GA;;;" + sid.String() + ")",
		InputBufferSize:    controlMessageLimit,
		OutputBufferSize:   controlMessageLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on daemon control pipe: %w", err)
	}

	var (
		once sync.Once
		wg   sync.WaitGroup
	)
	closeListener := func() {
		once.Do(func() {
			_ = listener.Close()
		})
	}
	go func() {
		<-ctx.Done()
		closeListener()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				requestCtx, cancel := context.WithTimeout(ctx, controlRequestTimeout)
				defer cancel()
				serveControlConnection(requestCtx, conn, handler)
			}()
		}
	}()
	return func() {
		closeListener()
		wg.Wait()
	}, nil
}

func sendControlRequest(ctx context.Context, pid int, request controlRequest) (controlResponse, error) {
	conn, err := winio.DialPipeContext(ctx, controlPipeName(pid))
	if err != nil {
		return controlResponse{}, fmt.Errorf("connect to daemon control pipe for pid %d: %w", pid, err)
	}
	if err := validateControlPipeServer(conn, pid); err != nil {
		_ = conn.Close()
		return controlResponse{}, err
	}
	return exchangeControlRequest(ctx, conn, request)
}

type controlPipeHandle interface {
	Fd() uintptr
}

func validateControlPipeServer(conn net.Conn, expectedPID int) error {
	handleConn, ok := conn.(controlPipeHandle)
	if !ok {
		return fmt.Errorf("daemon control connection type %T does not expose a pipe handle", conn)
	}
	var serverPID uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(handleConn.Fd()), &serverPID); err != nil {
		return fmt.Errorf("inspect daemon control pipe server: %w", err)
	}
	if int(serverPID) != expectedPID {
		return fmt.Errorf("daemon control pipe server pid %d does not match target pid %d", serverPID, expectedPID)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, serverPID)
	if err != nil {
		return fmt.Errorf("open daemon control pipe server pid %d: %w", serverPID, err)
	}
	defer windows.CloseHandle(process)
	if err := validateWindowsProcessHandle(process); err != nil {
		return fmt.Errorf("validate daemon control pipe server pid %d: %w", serverPID, err)
	}
	return nil
}
