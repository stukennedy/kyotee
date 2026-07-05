// Package state persists pipeline.State as JSON files, one per task, written
// atomically (temp + rename). Satisfies resume and audit without a DB.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stukennedy/kyotee/internal/pipeline"
)

// FileStore implements pipeline.Store under a directory
// (default ~/.kyotee/tasks/<taskID>.json).
type FileStore struct {
	Dir string
}

// DefaultDir returns ~/.kyotee/tasks, falling back to ./.kyotee/tasks.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kyotee", "tasks")
	}
	return filepath.Join(home, ".kyotee", "tasks")
}

func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return &FileStore{Dir: dir}, nil
}

func (s *FileStore) path(taskID string) string {
	// Task IDs are engine-generated, but sanitise anyway.
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, taskID)
	return filepath.Join(s.Dir, safe+".json")
}

func (s *FileStore) Save(st *pipeline.State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(st.TaskID))
}

func (s *FileStore) Load(taskID string) (*pipeline.State, error) {
	data, err := os.ReadFile(s.path(taskID))
	if err != nil {
		return nil, err
	}
	var st pipeline.State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("unmarshal state %s: %w", taskID, err)
	}
	if st.Meta == nil {
		st.Meta = map[string]string{}
	}
	return &st, nil
}

func (s *FileStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}
