// Package sse fans server-sent events out to the browsers watching one room.
// A room is a project: every board open on a project is one subscriber.
package sse

import (
	"log/slog"
	"sync"
)

// buffer is how far behind a browser may fall before we give up on it. A board
// mutation is a handful of bytes; a client that cannot drain sixteen of them has
// gone away without closing its connection.
const buffer = 16

// Event is one named SSE message carrying a rendered HTML fragment.
type Event struct {
	// Name is the SSE event name the client listens for ("card-moved").
	Name string
	// HTML is the payload. For surgical updates it contains hx-swap-oob
	// elements; for a board-dirty signal it may be empty.
	HTML string
	// ExceptClient, when set, is the tab that caused this change. It already
	// applied the update locally, so it is skipped.
	ExceptClient string
}

type client struct {
	id     string
	ch     chan Event
	closed bool
}

// Hub holds every live subscriber, grouped by room.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]map[*client]struct{}
	log   *slog.Logger
}

func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{rooms: make(map[string]map[*client]struct{}), log: log}
}

// Subscribe joins a room. The returned channel is closed when the subscriber is
// removed — either by calling the cancel func or because it fell too far behind.
// Callers must call cancel (defer it) even after the channel closes.
func (h *Hub) Subscribe(room, clientID string) (<-chan Event, func()) {
	c := &client{id: clientID, ch: make(chan Event, buffer)}

	h.mu.Lock()
	if h.rooms[room] == nil {
		h.rooms[room] = make(map[*client]struct{})
	}
	h.rooms[room][c] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() { h.remove(room, c) })
	}
	return c.ch, cancel
}

// Broadcast delivers an event to everyone in the room but the originating tab.
// It never blocks: a full buffer means that subscriber is gone, so it is
// dropped and its channel closed, which unblocks its handler.
func (h *Hub) Broadcast(room string, ev Event) {
	// The sends happen under the read lock on purpose. They cannot block — the
	// select has a default — and holding it excludes remove(), which is the only
	// thing that closes a channel. Snapshotting the subscribers and sending
	// after unlocking would race a concurrent drop into a send on a closed
	// channel.
	h.mu.RLock()
	var stalled []*client
	for c := range h.rooms[room] {
		if ev.ExceptClient != "" && c.id == ev.ExceptClient {
			continue
		}
		select {
		case c.ch <- ev:
		default:
			stalled = append(stalled, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range stalled {
		h.log.Warn("sse subscriber fell behind, dropping", "room", room, "client", c.id)
		h.remove(room, c)
	}
}

func (h *Hub) remove(room string, c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.rooms[room][c]; !ok {
		return
	}
	delete(h.rooms[room], c)
	if len(h.rooms[room]) == 0 {
		delete(h.rooms, room)
	}
	if !c.closed {
		c.closed = true
		close(c.ch)
	}
}

// Subscribers reports how many tabs are watching a room.
func (h *Hub) Subscribers(room string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms[room])
}

// Rooms reports how many rooms have at least one subscriber.
func (h *Hub) Rooms() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms)
}
