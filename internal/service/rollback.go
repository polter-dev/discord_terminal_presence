package service

import (
	"errors"
	"os"
)

type definitionSnapshot struct {
	data   []byte
	mode   os.FileMode
	exists bool
}

func snapshotDefinition(path string) (definitionSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return definitionSnapshot{}, nil
	}
	if err != nil {
		return definitionSnapshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return definitionSnapshot{}, err
	}
	return definitionSnapshot{data: data, mode: info.Mode(), exists: true}, nil
}

func restoreDefinition(path string, snapshot definitionSnapshot) error {
	if !snapshot.exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, snapshot.data, snapshot.mode.Perm())
}
