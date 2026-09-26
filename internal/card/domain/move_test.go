package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// board: backlog[a,b,c]  doing[d]  done[] (limit 2)
func testBoard() Board {
	return Board{
		LaneOfCard: map[string]string{
			"a": "backlog", "b": "backlog", "c": "backlog", "d": "doing",
		},
		Lanes: map[string]Lane{
			"backlog": {UUID: "backlog"},
			"doing":   {UUID: "doing", WIPLimit: 2},
			"done":    {UUID: "done"},
		},
	}
}

func TestPlan(t *testing.T) {
	tests := []struct {
		name string
		move Move
		want []Placement
		err  error
	}{
		{
			name: "reorder within a lane renumbers that lane densely",
			move: Move{CardUUID: "c", ToLane: "backlog", ToOrder: []string{"c", "a", "b"}},
			want: []Placement{
				{"c", "backlog", 0}, {"a", "backlog", 1}, {"b", "backlog", 2},
			},
		},
		{
			name: "cross-lane move rewrites both lanes",
			move: Move{
				CardUUID: "a", ToLane: "doing", ToOrder: []string{"d", "a"},
				FromLane: "backlog", FromOrder: []string{"b", "c"},
			},
			want: []Placement{
				{"d", "doing", 0}, {"a", "doing", 1},
				{"b", "backlog", 0}, {"c", "backlog", 1},
			},
		},
		{
			name: "a move into an empty lane is fine",
			move: Move{
				CardUUID: "d", ToLane: "done", ToOrder: []string{"d"},
				FromLane: "doing", FromOrder: []string{},
			},
			want: []Placement{{"d", "done", 0}},
		},
		{
			name: "same source and target lane is treated as a reorder",
			move: Move{
				CardUUID: "a", ToLane: "backlog", ToOrder: []string{"b", "a", "c"},
				FromLane: "backlog", FromOrder: []string{"b", "a", "c"},
			},
			want: []Placement{
				{"b", "backlog", 0}, {"a", "backlog", 1}, {"c", "backlog", 2},
			},
		},
		{
			name: "a card that is not on this board is refused",
			move: Move{CardUUID: "zz", ToLane: "backlog", ToOrder: []string{"zz"}},
			err:  ErrCardNotOnBoard,
		},
		{
			name: "a lane that is not on this board is refused",
			move: Move{CardUUID: "a", ToLane: "someone-elses-lane", ToOrder: []string{"a"}},
			err:  ErrLaneNotOnBoard,
		},
		{
			name: "an order naming a card from another project is refused",
			move: Move{CardUUID: "a", ToLane: "backlog", ToOrder: []string{"a", "intruder"}},
			err:  ErrCardNotOnBoard,
		},
		{
			name: "the moved card must appear in the new order",
			move: Move{CardUUID: "a", ToLane: "doing", ToOrder: []string{"d"}},
			err:  ErrCardMissing,
		},
		{
			name: "the same card twice in one order is refused",
			move: Move{CardUUID: "a", ToLane: "backlog", ToOrder: []string{"a", "a", "b"}},
			err:  ErrDuplicateInMove,
		},
		{
			name: "a card left in both the source and target order is refused",
			move: Move{
				CardUUID: "a", ToLane: "doing", ToOrder: []string{"d", "a"},
				FromLane: "backlog", FromOrder: []string{"a", "b", "c"},
			},
			err: ErrDuplicateInMove,
		},
		{
			name: "a WIP limit blocks an arrival",
			move: Move{
				CardUUID: "a", ToLane: "doing", ToOrder: []string{"d", "a", "b"},
				FromLane: "backlog", FromOrder: []string{"c"},
			},
			err: ErrWIPExceeded,
		},
		{
			name: "claiming the card came from a lane it is not in is refused",
			move: Move{
				CardUUID: "a", ToLane: "done", ToOrder: []string{"a"},
				FromLane: "doing", FromOrder: []string{"d"},
			},
			err: ErrOrderMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Plan(testBoard(), tt.move)
			if tt.err != nil {
				assert.ErrorIs(t, err, tt.err)
				assert.Nil(t, got, "a rejected move must not produce a partial write")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A lane already past its limit must stay usable, or a manager who lowers a
// limit would freeze the column for everyone.
func TestPlanAllowsReorderingALaneAlreadyOverItsLimit(t *testing.T) {
	b := Board{
		LaneOfCard: map[string]string{"a": "doing", "b": "doing", "c": "doing"},
		Lanes:      map[string]Lane{"doing": {UUID: "doing", WIPLimit: 2}},
	}
	got, err := Plan(b, Move{CardUUID: "c", ToLane: "doing", ToOrder: []string{"c", "a", "b"}})
	require.NoError(t, err)
	assert.Equal(t, []Placement{{"c", "doing", 0}, {"a", "doing", 1}, {"b", "doing", 2}}, got)
}

func TestAffectedLanesIsDeduplicatedAndOrdered(t *testing.T) {
	got := AffectedLanes([]Placement{
		{"d", "doing", 0}, {"a", "doing", 1}, {"b", "backlog", 0},
	})
	assert.Equal(t, []string{"doing", "backlog"}, got)
}
