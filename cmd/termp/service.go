package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/polter-dev/discord_terminal_presence/internal/service"
)

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := service.ResolveExecutable()
	if err != nil {
		return err
	}
	state, err := service.NewManager().Install(exe)
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	fmt.Printf("installed: %s\n", state.Path)
	fmt.Println("runs: termp start")
	fmt.Println("undo: termp uninstall")
	return nil
}

func uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := service.NewManager().Uninstall()
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Println(state.Message)
		return err
	}
	if err != nil {
		return err
	}
	if state.Path == "" {
		fmt.Println("not installed")
		return nil
	}
	fmt.Printf("removed: %s\n", state.Path)
	return nil
}
