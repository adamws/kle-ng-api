package failedtasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestNewDisabled(t *testing.T) {
	if New("", 10) != nil {
		t.Error("empty dir should disable the store")
	}
	if New(t.TempDir(), 0) != nil {
		t.Error("zero limit should disable the store")
	}
	var s *Store
	if path, err := s.Save(Record{TaskID: "x"}); path != "" || err != nil {
		t.Errorf("nil store Save = %q, %v; want no-op", path, err)
	}
}

func TestSaveWritesEntry(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(filepath.Join(logDir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(logDir, "build.log"), []byte("kbplacer output"), 0644)
	os.WriteFile(filepath.Join(logDir, "sub", "nested.log"), []byte("nested"), 0644)

	dir := filepath.Join(t.TempDir(), "failed")
	s := New(dir, 10)
	payload := []byte(`{"layout": {}, "settings": {"routing": "Full"}}`)
	started := time.Now().Add(-2 * time.Second)

	path, err := s.Save(Record{
		TaskID:    "abc-123",
		TaskType:  "generate_kicad_project",
		Payload:   payload,
		Err:       errors.New("kbplacer failed"),
		StartedAt: started,
		LogDir:    logDir,
		Env:       map[string]string{"kbplacerVersion": "v0.20"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Dir(path) != dir || !strings.HasSuffix(path, "_abc-123") {
		t.Errorf("unexpected entry path %q", path)
	}

	got, _ := os.ReadFile(filepath.Join(path, "request.json"))
	if string(got) != string(payload) {
		t.Errorf("request.json = %q, want payload verbatim", got)
	}

	var info failureInfo
	raw, _ := os.ReadFile(filepath.Join(path, "failure.json"))
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("failure.json: %v", err)
	}
	if info.TaskID != "abc-123" || info.Error != "kbplacer failed" || info.Env["kbplacerVersion"] != "v0.20" {
		t.Errorf("unexpected failure.json: %+v", info)
	}
	if info.DurationSeconds < 2 {
		t.Errorf("duration = %v, want >= 2s", info.DurationSeconds)
	}

	for file, want := range map[string]string{"build.log": "kbplacer output", "sub/nested.log": "nested"} {
		got, err := os.ReadFile(filepath.Join(path, "logs", file))
		if err != nil || string(got) != want {
			t.Errorf("logs/%s = %q, %v; want %q", file, got, err, want)
		}
	}
}

func TestSaveWithoutLogDir(t *testing.T) {
	s := New(t.TempDir(), 10)
	// A missing log directory (task failed before creating it) must not lose
	// the entry.
	path, err := s.Save(Record{TaskID: "t", Payload: []byte("{}"), LogDir: "/does/not/exist"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "request.json")); err != nil {
		t.Errorf("request.json missing: %v", err)
	}
}

func TestSaveKeepsNewestEntries(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, 3)
	for i := 0; i < 5; i++ {
		if _, err := s.Save(Record{TaskID: fmt.Sprintf("task%d", i), Payload: []byte("{}")}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	names := entryNames(t, dir)
	if len(names) != 3 {
		t.Fatalf("got %d entries %v, want 3", len(names), names)
	}
	for i, name := range names {
		if want := fmt.Sprintf("_task%d", i+2); !strings.HasSuffix(name, want) {
			t.Errorf("entry %d = %q, want suffix %q", i, name, want)
		}
	}
}

func TestPruneStaleTmpEntries(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, tmpPrefix+"stale")
	fresh := filepath.Join(dir, tmpPrefix+"fresh")
	os.Mkdir(stale, 0755)
	os.Mkdir(fresh, 0755)
	old := time.Now().Add(-2 * staleTmpAge)
	os.Chtimes(stale, old, old)

	if _, err := New(dir, 10).Save(Record{TaskID: "t", Payload: []byte("{}")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale temporary entry should be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh temporary entry (in progress elsewhere) must be kept")
	}
}

func TestCopyTailTruncates(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.log")
	os.WriteFile(src, []byte("0123456789"), 0644)
	dst := filepath.Join(dir, "out.log")
	if err := copyTail(src, dst, 4); err != nil {
		t.Fatalf("copyTail: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if !strings.HasPrefix(string(got), "...[truncated") || !strings.HasSuffix(string(got), "\n6789") {
		t.Errorf("copyTail = %q, want truncation note and last 4 bytes", got)
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"0b9c-AF_1": "0b9c-AF_1",
		"../etc":    "___etc",
		"":          "unknown",
	} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
