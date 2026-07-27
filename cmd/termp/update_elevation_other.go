//go:build !darwin && !linux

package main

func genericAutomaticUpdatePreflight() error {
	return nil
}
