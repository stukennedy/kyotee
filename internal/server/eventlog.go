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
// after an engine restart, when the in-memory bus is empty.
type eventLog struct {
	dir string
	mu  sync.Mutex
}

func newEventLog(dir string) *eventLog {
	return &eventLog{dir: dir}
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

// follow subscribes to all tasks and appends each event as one JSON line.
func (l *eventLog) follow(bus events.Bus) {
	ch, _ := bus.Subscribe("")
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
	f, err := os.OpenFile(l.path(ev.TaskID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
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
