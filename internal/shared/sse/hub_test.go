package sse

import (
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"runtime"
	"strconv"
	"time"
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

func TestRoomCountsReportsWhatIsWatching(t *testing.T) {
	// A subscriber that was never removed costs a goroutine and a buffered channel
	// and shows up nowhere. This is what makes the number queryable.
	h := quietHub()

	assert.Empty(t, h.RoomCounts())

	_, cancelA := h.Subscribe("board-1", "tab-a")
	_, cancelB := h.Subscribe("board-1", "tab-b")
	_, cancelC := h.Subscribe("board-2", "tab-c")

	counts := h.RoomCounts()
	assert.Equal(t, map[string]int{"board-1": 2, "board-2": 1}, counts)

	cancelA()
	assert.Equal(t, map[string]int{"board-1": 1, "board-2": 1}, h.RoomCounts())

	cancelB()
	cancelC()
	assert.Empty(t, h.RoomCounts(),
		"an empty room is removed, so an entry always means somebody is watching")
}

func TestRoomCountsIsASnapshot(t *testing.T) {
	// The caller must not be able to hold the lock while it renders, and must not see
	// the map change underneath it.
	h := quietHub()
	_, cancel := h.Subscribe("board-1", "tab-a")
	defer cancel()

	counts := h.RoomCounts()
	_, cancel2 := h.Subscribe("board-1", "tab-b")
	defer cancel2()

	assert.Equal(t, 1, counts["board-1"], "the snapshot did not change underneath us")
	assert.Equal(t, 2, h.RoomCounts()["board-1"])
}

// TestNoGoroutineLeakAfterUnsubscribe is the leak check the phase gate asks for.
//
// Every subscriber holds a goroutine in the streaming handler and a buffered channel.
// A cancel that does not actually free them is invisible until the process has
// thousands, so it is measured rather than assumed.
func TestNoGoroutineLeakAfterUnsubscribe(t *testing.T) {
	h := quietHub()

	// Settle first: the test binary's own goroutines come and go.
	settle()
	before := runtime.NumGoroutine()

	for i := range 200 {
		room := "board-" + strconv.Itoa(i%5)
		ch, cancel := h.Subscribe(room, "tab-"+strconv.Itoa(i))

		// A reader per subscriber, as the streaming handler has.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range ch {
			}
		}()

		h.Broadcast(room, Event{Name: "card-moved", HTML: "<div></div>"})
		cancel()
		<-done
	}

	assert.Empty(t, h.RoomCounts(), "every room should be gone")

	settle()
	after := runtime.NumGoroutine()
	assert.LessOrEqual(t, after, before+2,
		"goroutines grew from %d to %d across 200 subscribe/cancel cycles", before, after)
}

// settle gives finished goroutines a chance to be reaped before counting.
func settle() {
	for range 5 {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCancelAfterTheChannelClosedIsSafe(t *testing.T) {
	// The contract says callers must cancel even after the channel closes, which
	// happens when a subscriber is dropped for falling behind.
	h := quietHub()
	ch, cancel := h.Subscribe("board-1", "tab-a")

	// Overfill, so the hub drops it.
	for range buffer + 5 {
		h.Broadcast("board-1", Event{Name: "x", HTML: "y"})
	}

	// Drain to completion: the channel is closed once dropped.
	for range ch {
	}

	assert.NotPanics(t, cancel)
	assert.NotPanics(t, cancel, "and twice")
	assert.Empty(t, h.RoomCounts())
}

func TestBroadcastToAnEmptyRoomIsHarmless(t *testing.T) {
	// A mutation on a board nobody is watching is the normal case.
	h := quietHub()
	assert.NotPanics(t, func() {
		h.Broadcast("nobody-watching", Event{Name: "card-moved", HTML: "<div></div>"})
	})
}

func TestASignalWithNoPayloadIsDelivered(t *testing.T) {
	// board-dirty carries nothing: the client re-fetches the whole board rather than
	// swapping a fragment, because a lane arriving out of band cannot relocate an
	// element.
	h := quietHub()
	ch, cancel := h.Subscribe("board-1", "tab-a")
	defer cancel()

	h.Broadcast("board-1", Event{Name: "board-dirty"})

	select {
	case ev := <-ch:
		assert.Equal(t, "board-dirty", ev.Name)
		assert.Empty(t, ev.HTML)
	case <-time.After(time.Second):
		t.Fatal("the signal was not delivered")
	}
}
