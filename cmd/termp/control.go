package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	controlRequestTimeout = 12 * time.Second
	controlPollInterval   = 25 * time.Millisecond
	controlMessageLimit   = 4 << 10
)

type controlRequest struct {
	Command string `json:"command"`
	Force   bool   `json:"force,omitempty"`
}

type controlResponse struct {
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

type controlHandler func(context.Context, controlRequest) controlResponse

func serveControlConnection(ctx context.Context, conn net.Conn, handler controlHandler) {
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	var request controlRequest
	decoder := json.NewDecoder(io.LimitReader(conn, controlMessageLimit))
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(controlResponse{Error: fmt.Sprintf("decode control request: %v", err)})
		return
	}
	response := handler(ctx, request)
	_ = json.NewEncoder(conn).Encode(response)
}

func exchangeControlRequest(ctx context.Context, conn net.Conn, request controlRequest) (controlResponse, error) {
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return controlResponse{}, err
		}
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return controlResponse{}, fmt.Errorf("send daemon control request: %w", err)
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(conn, controlMessageLimit)).Decode(&response); err != nil {
		return controlResponse{}, fmt.Errorf("read daemon control response: %w", err)
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
