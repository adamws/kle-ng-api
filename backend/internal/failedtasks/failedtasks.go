// Package failedtasks keeps the most recent failed tasks on disk so a service
// admin can troubleshoot them later (over ssh) with everything needed to
// replicate the failure: the original request, the error and the build logs.
//
// Each failure is stored as its own directory named
// "<UTC timestamp>_<task id>" so a plain `ls` lists them oldest first:
//
//	20261031T120000.000000Z_<task id>/
//	  request.json  - task payload, exactly as POSTed to /api/pcb
//	  failure.json  - task id, error, timing and worker environment
//	  logs/         - copy of the task's logs directory (build.log, ...)
//
// Only the newest Limit entries are kept, older ones are deleted after every
// save. Several workers may share one directory: entries are assembled under a
// hidden temporary name and renamed into place, and pruning tolerates entries
// removed concurrently by another worker.
package failedtasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// timestampLayout is fixed width so entry names sort chronologically.
	timestampLayout = "20060102T150405.000000Z"

	// tmpPrefix marks entries still being written. Hidden from listings and
	// never counted towards the limit.
	tmpPrefix = ".tmp-"

	// staleTmpAge is how old a temporary entry must be before pruning treats
	// it as left over from a crashed worker and removes it.
	staleTmpAge = time.Hour

	// maxLogFileSize caps every copied log file. Larger files keep only their
	// tail, which is where the failure is.
	maxLogFileSize = 5 * 1024 * 1024
)

// Store saves failed tasks to Dir, keeping at most Limit entries.
// A nil *Store is valid and saves nothing.
type Store struct {
	dir   string
	limit int
}

// New returns a store writing to dir, or nil (disabled) when dir is empty or
// limit is not positive.
func New(dir string, limit int) *Store {
	if dir == "" || limit <= 0 {
		return nil
	}
	return &Store{dir: dir, limit: limit}
}

// Record describes one failed task.
type Record struct {
	TaskID   string
	TaskType string
	// Payload is the raw task payload, saved verbatim as request.json.
	Payload []byte
	Err     error
	// StartedAt is when the worker picked the task up.
	StartedAt time.Time
	// LogDir, when set, is copied into the entry as logs/.
	LogDir string
	// Env holds extra worker details (e.g. kbplacer version) worth having
	// when replicating the failure.
	Env map[string]string
}

// failureInfo is the content of failure.json.
type failureInfo struct {
	TaskID          string            `json:"taskId"`
	TaskType        string            `json:"taskType"`
	Error           string            `json:"error"`
	StartedAt       time.Time         `json:"startedAt"`
	FailedAt        time.Time         `json:"failedAt"`
	DurationSeconds float64           `json:"durationSeconds"`
	Hostname        string            `json:"hostname,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
}

// Save writes rec as a new entry and prunes old entries. It returns the path of
// the new entry. Calling Save on a nil store is a no-op.
func (s *Store) Save(rec Record) (string, error) {
	if s == nil {
		return "", nil
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create failed tasks directory: %w", err)
	}

	failedAt := time.Now().UTC()
	name := failedAt.Format(timestampLayout) + "_" + sanitize(rec.TaskID)
	tmpDir := filepath.Join(s.dir, tmpPrefix+name)
	finalDir := filepath.Join(s.dir, name)

	if err := s.write(tmpDir, rec, failedAt); err != nil {
		os.RemoveAll(tmpDir)
		return "", err
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("failed to finalize failed task entry: %w", err)
	}

	if err := s.prune(); err != nil {
		log.Printf("Failed to prune failed tasks directory: %v", err)
	}
	return finalDir, nil
}

func (s *Store) write(dir string, rec Record, failedAt time.Time) error {
	if err := os.Mkdir(dir, 0755); err != nil {
		return fmt.Errorf("failed to create failed task entry: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "request.json"), rec.Payload, 0644); err != nil {
		return fmt.Errorf("failed to write request.json: %w", err)
	}

	info := failureInfo{
		TaskID:    rec.TaskID,
		TaskType:  rec.TaskType,
		StartedAt: rec.StartedAt.UTC(),
		FailedAt:  failedAt,
		Env:       rec.Env,
	}
	if rec.Err != nil {
		info.Error = rec.Err.Error()
	}
	if !rec.StartedAt.IsZero() {
		info.DurationSeconds = failedAt.Sub(rec.StartedAt).Seconds()
	}
	info.Hostname, _ = os.Hostname()

	infoJSON, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal failure.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "failure.json"), infoJSON, 0644); err != nil {
		return fmt.Errorf("failed to write failure.json: %w", err)
	}

	if rec.LogDir != "" {
		// Logs are best-effort: request and error alone still allow replaying
		// the task, so a copy problem must not lose the entry.
		if _, err := os.Stat(rec.LogDir); errors.Is(err, fs.ErrNotExist) {
			// Task failed before creating its logs, nothing to copy
		} else if err := copyLogs(rec.LogDir, filepath.Join(dir, "logs")); err != nil {
			log.Printf("[Task %s] Failed to copy logs to failed task entry: %v", rec.TaskID, err)
		}
	}
	return nil
}

// prune deletes the oldest entries beyond the limit and temporary entries left
// behind by crashed workers.
func (s *Store) prune() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleTmpAge {
				os.RemoveAll(filepath.Join(s.dir, e.Name()))
			}
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
	}

	if len(names) <= s.limit {
		return nil
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names[:len(names)-s.limit] {
		if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// copyLogs copies regular files from src into dst, keeping the directory
// structure. Files larger than maxLogFileSize keep only their tail.
func copyLogs(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyTail(path, target, maxLogFileSize)
	})
}

func copyTail(src, dst string, maxSize int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	stat, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if stat.Size() > maxSize {
		if _, err := in.Seek(stat.Size()-maxSize, io.SeekStart); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "...[truncated, kept last %d of %d bytes]...\n", maxSize, stat.Size()); err != nil {
			return err
		}
	}
	_, err = io.Copy(out, io.LimitReader(in, maxSize))
	return err
}

// sanitize keeps task IDs (UUIDs from asynq) safe to use as a path component.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
	if s == "" {
		return "unknown"
	}
	return s
}
