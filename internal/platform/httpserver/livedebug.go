package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/AzmainMahtab/chonkboard/internal/shared/authctx"
	"github.com/AzmainMahtab/chonkboard/internal/shared/sse"
)

// liveStatus reports how many boards have watchers and how many watchers each has.
//
// A leak in the hub is otherwise invisible: a subscriber that was never removed costs
// one goroutine and one buffered channel, shows up nowhere, and only becomes obvious
// when the process has thousands of them. This makes the number queryable, which is
// the whole reason it exists.
//
// Operator only, and deliberately not on /healthz: room counts say how many projects
// are in use and how many people are looking at them, which is not something to publish
// to an unauthenticated endpoint.
func liveStatus(hub *sse.Hub) http.HandlerFunc {
	type room struct {
		ProjectUUID string `json:"project_uuid"`
		Subscribers int    `json:"subscribers"`
	}
	type response struct {
		Rooms            int    `json:"rooms"`
		TotalSubscribers int    `json:"total_subscribers"`
		Detail           []room `json:"detail"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		user := authctx.User(r.Context())
		if user == nil || !user.IsSuperAdmin() {
			// 404 rather than 403: a debug surface should not advertise itself.
			http.NotFound(w, r)
			return
		}

		counts := hub.RoomCounts()
		out := response{Rooms: len(counts), Detail: make([]room, 0, len(counts))}
		for projectUUID, subscribers := range counts {
			out.TotalSubscribers += subscribers
			out.Detail = append(out.Detail, room{
				ProjectUUID: projectUUID, Subscribers: subscribers,
			})
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	}
}
