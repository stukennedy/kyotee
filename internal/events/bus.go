package events

import (
	"sync"
	"time"
)

// MemBus is the in-memory Bus implementation. It assigns per-task monotonic
// sequence numbers, retains history for replay, and never blocks publishers:
// a slow subscriber's channel is drained oldest-first when full.
type MemBus struct {
	mu      sync.Mutex
	seq     map[string]int64
	history map[string][]Event
	subs    map[int]*subscriber
	nextSub int
}

type subscriber struct {
	taskID string // "" = all tasks
	ch     chan Event
}

const subBuffer = 1024

func NewBus() *MemBus {
	return &MemBus{
		seq:     make(map[string]int64),
		history: make(map[string][]Event),
		subs:    make(map[int]*subscriber),
	}
}

func (b *MemBus) Publish(ev Event) {
	b.mu.Lock()
	ev.Seq = b.seq[ev.TaskID]
	b.seq[ev.TaskID]++
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	b.history[ev.TaskID] = append(b.history[ev.TaskID], ev)
	subs := make([]*subscriber, 0, len(b.subs))
	for _, s := range b.subs {
		if s.taskID == "" || s.taskID == ev.TaskID {
			subs = append(subs, s)
		}
	}
	b.mu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			// Drop oldest to make room; publishers must never block.
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
}

func (b *MemBus) Subscribe(taskID string) (<-chan Event, func()) {
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	// Size the buffer to hold the full replay so history can be loaded while
	// the lock is held — no publish can interleave, ordering is preserved.
	replay := b.history[taskID]
	s := &subscriber{taskID: taskID, ch: make(chan Event, len(replay)+subBuffer)}
	if taskID != "" {
		for _, ev := range replay {
			s.ch <- ev
		}
	}
	b.subs[id] = s
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
	return s.ch, cancel
}

// SeedSeq advances a task's next sequence number (never backwards). Used
// when resuming a task after an engine restart so new events continue after
// the persisted event log instead of colliding at Seq 0.
func (b *MemBus) SeedSeq(taskID string, next int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seq[taskID] < next {
		b.seq[taskID] = next
	}
}

func (b *MemBus) History(taskID string) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.history[taskID]))
	copy(out, b.history[taskID])
	return out
}

// EmitterFor returns an Emitter that stamps TaskID before publishing.
func EmitterFor(b Bus, taskID string) Emitter {
	return func(ev Event) {
		ev.TaskID = taskID
		b.Publish(ev)
	}
}
