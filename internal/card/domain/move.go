// Package domain holds card rules with no knowledge of HTTP, SQL or templates.
package domain

import (
	"fmt"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Errors a move can fail with. Handlers compare against these, never against
// message text.
var (
	ErrCardNotOnBoard  = apperrors.NotFound("That card is not on this board.")
	ErrLaneNotOnBoard  = apperrors.NotFound("That lane is not on this board.")
	ErrCardMissing     = apperrors.Invalid("The new order is missing the card being moved.")
	ErrOrderMismatch   = apperrors.Conflict("The board changed while you were dragging. It has been refreshed.")
	ErrDuplicateInMove = apperrors.Invalid("The new order lists the same card twice.")
	ErrWIPExceeded     = apperrors.Conflict("That lane is already at its limit.")
)

// Lane is the little a move needs to know about a column.
type Lane struct {
	UUID     string
	WIPLimit int // 0 means no limit
}

// Board is a snapshot of where every card currently sits. It is built by the
// service from one read, so the whole move can be planned before a write.
type Board struct {
	// LaneOfCard maps a card's uuid to the lane it is in right now.
	LaneOfCard map[string]string
	// Lanes is every lane on this board, by uuid.
	Lanes map[string]Lane
}

// Move is a requested drag: the card that moved, and the resulting order of the
// lane it landed in — plus the source lane's new order when it came from
// elsewhere, because removing a card shifts everything below it.
type Move struct {
	CardUUID  string
	ToLane    string
	ToOrder   []string
	FromLane  string // empty when the card did not change lane
	FromOrder []string
}

// Placement is one row to write: this card belongs in this lane at this index.
type Placement struct {
	CardUUID string
	LaneUUID string
	Position int
}

// Plan validates a move against the board and returns the exact set of rows to
// write. It is pure: every rule below is checked before anything is persisted, so
// a rejected move never leaves the board half-reordered.
//
// Positions come out dense and zero-based. The client sends the order it is
// already showing, so agreeing with it is the whole contract — there is no
// fractional ranking to drift or rebalance.
func Plan(b Board, m Move) ([]Placement, error) {
	if _, ok := b.LaneOfCard[m.CardUUID]; !ok {
		return nil, ErrCardNotOnBoard
	}
	toLane, ok := b.Lanes[m.ToLane]
	if !ok {
		return nil, ErrLaneNotOnBoard
	}

	sameLane := m.FromLane == "" || m.FromLane == m.ToLane
	if !sameLane {
		if _, ok := b.Lanes[m.FromLane]; !ok {
			return nil, ErrLaneNotOnBoard
		}
		// The card must actually have come from the lane the client names,
		// otherwise two people dragging at once could write each other's order.
		if b.LaneOfCard[m.CardUUID] != m.FromLane {
			return nil, ErrOrderMismatch
		}
	}

	if !contains(m.ToOrder, m.CardUUID) {
		return nil, ErrCardMissing
	}

	// A WIP limit counts what the lane will hold once the move lands. A lane
	// already over its limit can still be reordered — the limit gates arrivals,
	// it does not freeze the column.
	if toLane.WIPLimit > 0 && !sameLane && len(m.ToOrder) > toLane.WIPLimit {
		return nil, ErrWIPExceeded
	}

	seen := make(map[string]struct{}, len(m.ToOrder)+len(m.FromOrder))
	var out []Placement

	appendLane := func(lane string, order []string) error {
		for i, uuid := range order {
			if _, dup := seen[uuid]; dup {
				return ErrDuplicateInMove
			}
			seen[uuid] = struct{}{}

			current, known := b.LaneOfCard[uuid]
			if !known {
				return ErrCardNotOnBoard
			}
			// Every card named in an order must already be in one of the two
			// lanes this move touches. Anything else means the client is working
			// from a stale board, or reaching into a lane it did not drag in.
			if current != lane && current != m.ToLane && current != m.FromLane {
				return ErrOrderMismatch
			}
			out = append(out, Placement{CardUUID: uuid, LaneUUID: lane, Position: i})
		}
		return nil
	}

	if err := appendLane(m.ToLane, m.ToOrder); err != nil {
		return nil, err
	}
	if !sameLane {
		if contains(m.FromOrder, m.CardUUID) {
			return nil, ErrDuplicateInMove
		}
		if err := appendLane(m.FromLane, m.FromOrder); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AffectedLanes lists the lanes a plan rewrote, so the caller knows which
// fragments to re-render and broadcast.
func AffectedLanes(p []Placement) []string {
	seen := make(map[string]struct{}, 2)
	var out []string
	for _, pl := range p {
		if _, ok := seen[pl.LaneUUID]; ok {
			continue
		}
		seen[pl.LaneUUID] = struct{}{}
		out = append(out, pl.LaneUUID)
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (m Move) String() string {
	return fmt.Sprintf("card %s -> lane %s (%d cards)", m.CardUUID, m.ToLane, len(m.ToOrder))
}
