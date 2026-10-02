package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type checkState struct {
	CheckedAt       time.Time `json:"checked_at"`
	LatestVersion   string    `json:"latest_version"`
	ReleaseURL      string    `json:"release_url"`
	NotifiedAt      time.Time `json:"notified_at"`
	NotifiedVersion string    `json:"notified_version"`
}

func loadState(path string) (*checkState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state checkState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveState(path string, state *checkState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".update-*.json")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
