package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func limit(n int) *int { return &n }

func TestColourPaletteIsClosed(t *testing.T) {
	// The palette is closed because a lane colour has to match a Tailwind class
	// that exists at build time. A hex code would render an unstyled lane with
	// no error anywhere.
	for _, c := range Colours {
		assert.True(t, c.Valid(), "%s should be in the palette", c)
	}
	assert.Len(t, Colours, 7)

	for _, c := range []Colour{"", "#ff0000", "red", "Slate", "indigo"} {
		assert.False(t, c.Valid(), "%q must not be accepted", c)
	}
}

func TestNewLane(t *testing.T) {
	l, err := NewLane("l-1", "p-1", "  In progress  ", 2, ColourAmber, limit(3), false, testNow)

	require.NoError(t, err)
	assert.Equal(t, "In progress", l.Name, "trimmed")
	assert.Equal(t, 2, l.Position)
	assert.Equal(t, 3, l.Limit())
	assert.False(t, l.IsDone)
}

func TestLaneLimitTreatsNilAsUnlimited(t *testing.T) {
	// The move planner wants zero for "no limit"; the column wants NULL. This is
	// where those two representations meet.
	unlimited, err := NewLane("l", "p", "backlog", 0, ColourSlate, nil, false, testNow)
	require.NoError(t, err)
	assert.Nil(t, unlimited.WIPLimit)
	assert.Zero(t, unlimited.Limit())

	capped, err := NewLane("l", "p", "doing", 0, ColourSlate, limit(5), false, testNow)
	require.NoError(t, err)
	assert.Equal(t, 5, capped.Limit())
}

func TestNewLaneValidation(t *testing.T) {
	tests := []struct {
		name       string
		uuid       string
		project    string
		laneName   string
		position   int
		colour     Colour
		wip        *int
		wantFields []string
	}{
		{
			name: "everything missing", colour: Colour(""),
			wantFields: []string{"uuid", "project_uuid", "name", "colour"},
		},
		{
			name: "negative position", uuid: "l", project: "p", laneName: "n",
			position: -1, colour: ColourSlate,
			wantFields: []string{"position"},
		},
		{
			name: "name too long", uuid: "l", project: "p",
			laneName: strings.Repeat("x", MaxLaneNameLength+1), colour: ColourSlate,
			wantFields: []string{"name"},
		},
		{
			name: "zero wip limit", uuid: "l", project: "p", laneName: "n",
			colour: ColourSlate, wip: limit(0),
			wantFields: []string{"wip_limit"},
		},
		{
			name: "negative wip limit", uuid: "l", project: "p", laneName: "n",
			colour: ColourSlate, wip: limit(-1),
			wantFields: []string{"wip_limit"},
		},
		{
			name: "colour outside the palette", uuid: "l", project: "p", laneName: "n",
			colour:     Colour("#bada55"),
			wantFields: []string{"colour"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewLane(tc.uuid, tc.project, tc.laneName, tc.position,
				tc.colour, tc.wip, false, testNow)

			require.Error(t, err)
			assert.Nil(t, l)
			assert.ErrorIs(t, err, apperrors.New(apperrors.CodeValidation, ""))

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}

func TestLaneUpdate(t *testing.T) {
	later := testNow.Add(time.Hour)
	l, err := NewLane("l", "p", "doing", 1, ColourSlate, nil, false, testNow)
	require.NoError(t, err)

	require.NoError(t, l.Update("  Doing  ", ColourTeal, limit(2), true, later))
	assert.Equal(t, "Doing", l.Name)
	assert.Equal(t, ColourTeal, l.Colour)
	assert.Equal(t, 2, l.Limit())
	assert.True(t, l.IsDone)
	assert.Equal(t, 1, l.Position, "an update must not move the lane")

	t.Run("a rejected update changes nothing", func(t *testing.T) {
		require.Error(t, l.Update("", ColourTeal, nil, true, later))
		assert.Equal(t, "Doing", l.Name)

		require.Error(t, l.Update("Doing", Colour("puce"), nil, true, later))
		assert.Equal(t, ColourTeal, l.Colour)

		require.Error(t, l.Update("Doing", ColourTeal, limit(0), true, later))
		assert.Equal(t, 2, l.Limit())
	})
}

func TestReorder(t *testing.T) {
	t.Run("assigns dense zero-based positions", func(t *testing.T) {
		got, err := Reorder([]string{"c", "a", "b"})
		require.NoError(t, err)
		assert.Equal(t, map[string]int{"c": 0, "a": 1, "b": 2}, got)
	})

	t.Run("refuses an empty order", func(t *testing.T) {
		_, err := Reorder(nil)
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
	})

	t.Run("refuses a repeated lane", func(t *testing.T) {
		_, err := Reorder([]string{"a", "b", "a"})
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
	})

	t.Run("refuses an empty lane id", func(t *testing.T) {
		_, err := Reorder([]string{"a", ""})
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
	})
}

func TestNewLabel(t *testing.T) {
	l, err := NewLabel("lb-1", "p-1", "  bug  ", ColourRose, testNow)

	require.NoError(t, err)
	assert.Equal(t, "bug", l.Name)
	assert.Equal(t, ColourRose, l.Colour)
}

func TestNewLabelValidation(t *testing.T) {
	tests := []struct {
		name       string
		uuid       string
		project    string
		labelName  string
		colour     Colour
		wantFields []string
	}{
		{
			name: "everything missing", colour: Colour(""),
			wantFields: []string{"uuid", "project_uuid", "name", "colour"},
		},
		{
			name: "name too long", uuid: "l", project: "p",
			labelName: strings.Repeat("x", MaxLabelNameLength+1), colour: ColourSlate,
			wantFields: []string{"name"},
		},
		{
			name: "colour outside the palette", uuid: "l", project: "p",
			labelName: "bug", colour: Colour("neon"),
			wantFields: []string{"colour"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewLabel(tc.uuid, tc.project, tc.labelName, tc.colour, testNow)

			require.Error(t, err)
			assert.Nil(t, l)

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}

func TestLabelRename(t *testing.T) {
	l, err := NewLabel("lb", "p", "bug", ColourRose, testNow)
	require.NoError(t, err)

	require.NoError(t, l.Rename("  defect  ", ColourAmber, testNow))
	assert.Equal(t, "defect", l.Name)
	assert.Equal(t, ColourAmber, l.Colour)

	require.Error(t, l.Rename("", ColourAmber, testNow))
	assert.Equal(t, "defect", l.Name, "a rejected rename changes nothing")
}
