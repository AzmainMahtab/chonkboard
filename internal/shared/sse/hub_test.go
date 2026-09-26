package sse

import (
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietHub() *Hub {
	return NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestBroadcastReachesOtherSubscribers(t *testing.T) {
	h := quietHub()
	a, cancelA := h.Subscribe("p1", "tab-a")
	b, cancelB := h.Subscribe("p1", "tab-b")
	defer cancelA()
	defer cancelB()

	h.Broadcast("p1", Event{Name: "card-moved", HTML: "<div/>"})

	assert.Equal(t, "card-moved", (<-a).Name)
	assert.Equal(t, "card-moved", (<-b).Name)
}

func TestBroadcastSkipsOriginatingTab(t *testing.T) {
	h := quietHub()
	mover, cancelMover := h.Subscribe("p1", "tab-a")
	other, cancelOther := h.Subscribe("p1", "tab-b")
	defer cancelMover()
	defer cancelOther()

	h.Broadcast("p1", Event{Name: "card-moved", ExceptClient: "tab-a"})

	assert.Equal(t, "card-moved", (<-other).Name)
	assert.Empty(t, mover, "the tab that made the change must not be echoed to")
}

func TestBroadcastDoesNotCrossRooms(t *testing.T) {
	h := quietHub()
	p1, cancel1 := h.Subscribe("p1", "tab-a")
	p2, cancel2 := h.Subscribe("p2", "tab-b")
	defer cancel1()
	defer cancel2()

	h.Broadcast("p1", Event{Name: "card-moved"})

	assert.Len(t, p1, 1)
	assert.Empty(t, p2, "a project's events must never leak into another board")
}

func TestSlowSubscriberIsDroppedNotBlockedOn(t *testing.T) {
	h := quietHub()
	slow, cancel := h.Subscribe("p1", "tab-slow")
	defer cancel()

	// Never drain. One more than the buffer must not block the writer.
	for i := 0; i < buffer+1; i++ {
		h.Broadcast("p1", Event{Name: "card-moved"})
	}

	assert.Equal(t, 0, h.Subscribers("p1"), "an undrained subscriber is dropped")
	drained := 0
	for range slow {
		drained++
	}
	assert.Equal(t, buffer, drained, "its channel is closed so the handler exits")
}

func TestCancelIsIdempotentAndFreesTheRoom(t *testing.T) {
	h := quietHub()
	ch, cancel := h.Subscribe("p1", "tab-a")
	require.Equal(t, 1, h.Subscribers("p1"))

	cancel()
	cancel() // must not panic on a double close

	_, open := <-ch
	assert.False(t, open, "cancel closes the channel")
	assert.Equal(t, 0, h.Rooms(), "an empty room is reclaimed")
}

func TestConcurrentSubscribeBroadcastAndCancel(t *testing.T) {
	h := quietHub()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ch, cancel := h.Subscribe("p1", "tab")
			defer cancel()
			go h.Broadcast("p1", Event{Name: "card-moved"})
			select {
			case <-ch:
			default:
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 0, h.Subscribers("p1"))
}
