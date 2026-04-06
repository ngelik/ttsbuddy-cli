package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LastJob represents the most recently submitted job.
type LastJob struct {
	JobID     string    `json:"job_id"`
	Timestamp time.Time `json:"timestamp"`
}

// SaveLastJob persists the job ID for `ttsbuddy status` without arguments.
// Call only after the API has accepted the request and returned a job_id.
func SaveLastJob(jobID string) error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}

	lj := LastJob{
		JobID:     jobID,
		Timestamp: time.Now().UTC(),
	}

	data, err := json.MarshalIndent(lj, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal last job: %w", err)
	}
	data = append(data, '\n')

	path := filepath.Join(dir, "last_job.json")
	return atomicWriteFile(path, data, 0600)
}

// LoadLastJob reads the most recent job.
// Returns (nil, nil) if the file doesn't exist (normal).
// Returns (nil, error) for real failures (permission denied, corruption).
func LoadLastJob() (*LastJob, error) {
	dir, err := configDir()
	if err != nil {
		return nil, fmt.Errorf("config directory: %w", err)
	}

	path := filepath.Join(dir, "last_job.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // normal: no job saved yet
		}
		return nil, fmt.Errorf("reading last job: %w", err)
	}

	var lj LastJob
	if err := json.Unmarshal(data, &lj); err != nil {
		return nil, fmt.Errorf("parsing last job: %w", err)
	}
	if lj.JobID == "" {
		return nil, nil
	}
	return &lj, nil
}
