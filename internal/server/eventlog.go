package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/stukennedy/kyotee/internal/events"
)

// eventLog persists every event to a per-task append-only ndjson file next
// to the state file (spec 02 §3), so /events can replay a full run even
// after an engine restart, when the in-memory bus is empty. File handles are
// cached per task and closed when the task reaches a terminal event, so the
// hot path is one write, not open/write/close.
type eventLog struct {
	dir   string
	mu    sync.Mutex
	files map[string]*os.File
}

func newEventLog(dir string) *eventLog {
	return &eventLog{dir: dir, files: map[string]*os.File{}}
}

func (l *eventLog) path(taskID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, taskID)
	return filepath.Join(l.dir, safe+".events.ndjson")
}

// drain appends each event from an already-open subscription as one JSON
// line. The caller subscribes BEFORE any task can publish (engine startup),
// so the log never misses head events.
func (l *eventLog) drain(ch <-chan events.Event) {
	for ev := range ch {
		l.append(ev)
	}
}

func (l *eventLog) append(ev events.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.files[ev.TaskID]
	if !ok {
		f, err = os.OpenFile(l.path(ev.TaskID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		l.files[ev.TaskID] = f
	}
	f.Write(append(data, '\n'))
	if terminalEvent(ev) {
		f.Close()
		delete(l.files, ev.TaskID)
	}
}

// read returns the persisted events for a task, in file (Seq) order.
func (l *eventLog) read(taskID string) []events.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path(taskID))
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []events.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev events.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err == nil {
			out = append(out, ev)
		}
	}
	return out
}
