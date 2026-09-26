package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

var cardNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func TestPrioritySetIsClosed(t *testing.T) {
	for _, p := range Priorities {
		assert.True(t, p.Valid(), "%s should be a known priority", p)
	}
	assert.Len(t, Priorities, 5)

	for _, p := range []Priority{"", "critical", "None", "9"} {
		assert.False(t, p.Valid(), "%q must not be accepted", p)
	}
}

func TestNewCard(t *testing.T) {
	c, err := NewCard("c-1", "p-1", "l-1", "  Ship the thing  ", "u-1", 3, cardNow)

	require.NoError(t, err)
	assert.Equal(t, "Ship the thing", c.Title, "trimmed")
	assert.Equal(t, 3, c.Position)
	assert.Equal(t, PriorityNone, c.Priority, "a new card has no priority")
	assert.Empty(t, c.Description)
	assert.Nil(t, c.AssigneeUUID)
	assert.Nil(t, c.DueAt)
	assert.False(t, c.IsArchived())
}

func TestNewCardValidation(t *testing.T) {
	tests := []struct {
		name       string
		uuid       string
		project    string
		lane       string
		title      string
		createdBy  string
		position   int
		wantFields []string
	}{
		{
			name:       "everything missing",
			wantFields: []string{"uuid", "project_uuid", "lane_uuid", "title", "created_by"},
		},
		{
			name: "blank title", uuid: "c", project: "p", lane: "l",
			title: "   ", createdBy: "u",
			wantFields: []string{"title"},
		},
		{
			name: "title too long", uuid: "c", project: "p", lane: "l",
			title: strings.Repeat("x", MaxTitleLength+1), createdBy: "u",
			wantFields: []string{"title"},
		},
		{
			name: "negative position", uuid: "c", project: "p", lane: "l",
			title: "t", createdBy: "u", position: -1,
			wantFields: []string{"position"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewCard(tc.uuid, tc.project, tc.lane, tc.title, tc.createdBy,
				tc.position, cardNow)

			require.Error(t, err)
			assert.Nil(t, c)
			assert.ErrorIs(t, err, apperrors.New(apperrors.CodeValidation, ""))

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}

func TestCardEdit(t *testing.T) {
	later := cardNow.Add(time.Hour)
	c, err := NewCard("c", "p", "l", "before", "u", 0, cardNow)
	require.NoError(t, err)

	assignee := "u-2"
	due := time.Date(2026, 12, 25, 9, 30, 0, 0, time.UTC)
	require.NoError(t, c.Edit("  after  ", "  body  ", PriorityHigh, &assignee, &due, later))

	assert.Equal(t, "after", c.Title)
	assert.Equal(t, "body", c.Description)
	assert.Equal(t, PriorityHigh, c.Priority)
	require.NotNil(t, c.AssigneeUUID)
	assert.Equal(t, "u-2", *c.AssigneeUUID)
	require.NotNil(t, c.DueAt)
	assert.Equal(t, due, *c.DueAt)
	assert.Equal(t, later, c.UpdatedAt)
	assert.Equal(t, "l", c.LaneUUID, "an edit must not move the card")
	assert.Equal(t, 0, c.Position, "an edit must not reposition the card")
}

func TestCardEditClearsOptionalFields(t *testing.T) {
	c, err := NewCard("c", "p", "l", "card", "u", 0, cardNow)
	require.NoError(t, err)

	assignee := "u-2"
	due := cardNow.Add(time.Hour)
	require.NoError(t, c.Edit("card", "", PriorityLow, &assignee, &due, cardNow))
	require.NotNil(t, c.AssigneeUUID)

	require.NoError(t, c.Edit("card", "", PriorityNone, nil, nil, cardNow))
	assert.Nil(t, c.AssigneeUUID, "nil must clear, not be ignored")
	assert.Nil(t, c.DueAt)
}

func TestCardEditNormalisesTheDueDateToUTC(t *testing.T) {
	c, err := NewCard("c", "p", "l", "card", "u", 0, cardNow)
	require.NoError(t, err)

	zone := time.FixedZone("UTC+6", 6*60*60)
	due := time.Date(2026, 7, 8, 18, 0, 0, 0, zone)
	require.NoError(t, c.Edit("card", "", PriorityNone, nil, &due, cardNow))

	require.NotNil(t, c.DueAt)
	assert.Equal(t, time.UTC, c.DueAt.Location())
	assert.True(t, due.Equal(*c.DueAt))
}

func TestCardEditValidation(t *testing.T) {
	empty := ""
	tests := []struct {
		name       string
		title      string
		desc       string
		priority   Priority
		assignee   *string
		wantFields []string
	}{
		{
			name: "blank title", title: "  ", priority: PriorityNone,
			wantFields: []string{"title"},
		},
		{
			name: "description too long", title: "t", priority: PriorityNone,
			desc:       strings.Repeat("x", MaxDescriptionLength+1),
			wantFields: []string{"description"},
		},
		{
			name: "unknown priority", title: "t", priority: Priority("critical"),
			wantFields: []string{"priority"},
		},
		{
			name: "assignee present but empty", title: "t", priority: PriorityNone,
			assignee:   &empty,
			wantFields: []string{"assignee"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewCard("c", "p", "l", "original", "u", 0, cardNow)
			require.NoError(t, err)

			err = c.Edit(tc.title, tc.desc, tc.priority, tc.assignee, nil, cardNow)

			require.Error(t, err)
			assert.Equal(t, "original", c.Title, "a rejected edit changes nothing")

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}

func TestCardArchiveIsIdempotentAndReversible(t *testing.T) {
	c, err := NewCard("c", "p", "l", "card", "u", 0, cardNow)
	require.NoError(t, err)

	c.Archive(cardNow.Add(time.Hour))
	require.True(t, c.IsArchived())
	first := *c.ArchivedAt

	c.Archive(cardNow.Add(2 * time.Hour))
	assert.Equal(t, first, *c.ArchivedAt, "the first archive time stands")

	c.Restore(cardNow.Add(3 * time.Hour))
	assert.False(t, c.IsArchived())
	assert.Nil(t, c.ArchivedAt)
}

func TestIsOverdue(t *testing.T) {
	past := cardNow.Add(-time.Hour)
	future := cardNow.Add(time.Hour)

	tests := []struct {
		name       string
		due        *time.Time
		laneIsDone bool
		want       bool
	}{
		{"no due date is never overdue", nil, false, false},
		{"due in the future", &future, false, false},
		{"due in the past", &past, false, true},
		{"due in the past but the lane is done", &past, true, false},
		{"no due date in a done lane", nil, true, false},
		{"due exactly now is not yet overdue", &cardNow, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewCard("c", "p", "l", "card", "u", 0, cardNow)
			require.NoError(t, err)
			c.DueAt = tc.due

			assert.Equal(t, tc.want, c.IsOverdue(cardNow, tc.laneIsDone))
		})
	}
}
