package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const cacheFileName = "update-check.json"

type cacheState struct {
	CheckedAt     time.Time `json:"checkedAt,omitempty"`
	LatestVersion string    `json:"latestVersion,omitempty"`
}

func loadCache(path string) (cacheState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cacheState{}, nil
		}
		return cacheState{}, err
	}
	var state cacheState
	if err := json.Unmarshal(data, &state); err != nil {
		return cacheState{}, err
	}
	return state, nil
}

func saveCache(path string, state cacheState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
