package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Priority is how urgent a card is. The closed set matches the schema's CHECK and
// the left-edge colour bar in web/components/card.templ.
type Priority string

const (
	// PriorityNone is the default and draws no bar.
	PriorityNone   Priority = "none"
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// Priorities is the set in ascending order, for a select.
var Priorities = []Priority{
	PriorityNone, PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent,
}

// Valid reports whether p is one of the priorities the schema allows.
func (p Priority) Valid() bool {
	for _, known := range Priorities {
		if p == known {
			return true
		}
	}
	return false
}

// Field bounds. The schema requires a non-empty title; the rest are domain rules.
const (
	MaxTitleLength       = 200
	MaxDescriptionLength = 20000
)

// Card is one item on a board.
//
// ProjectUUID is denormalised from the lane. It is what makes every
// authorisation check — "is this card on a board this person was granted?" — a
// single lookup instead of a join, and it is why a move must verify the target
// lane belongs to the same project rather than trusting the client.
type Card struct {
	UUID         string
	ProjectUUID  string
	LaneUUID     string
	Title        string
	Description  string
	Position     int
	Priority     Priority
	AssigneeUUID *string
	DueAt        *time.Time
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ArchivedAt   *time.Time
}

// NewCard validates and builds a card. Position is assigned by the store, which
// appends to the end of the lane inside the same transaction that inserts.
func NewCard(uuid, projectUUID, laneUUID, title, createdBy string, position int, now time.Time) (*Card, error) {
	title = strings.TrimSpace(title)

	err := apperrors.Validation("that card is not valid")
	invalid := false

	if uuid == "" {
		err = err.WithField("uuid", "is required")
		invalid = true
	}
	if projectUUID == "" {
		err = err.WithField("project_uuid", "is required")
		invalid = true
	}
	if laneUUID == "" {
		err = err.WithField("lane_uuid", "is required")
		invalid = true
	}
	if problem := validateTitle(title); problem != "" {
		err = err.WithField("title", problem)
		invalid = true
	}
	if createdBy == "" {
		err = err.WithField("created_by", "is required")
		invalid = true
	}
	if position < 0 {
		err = err.WithField("position", "must not be negative")
		invalid = true
	}
	if invalid {
		return nil, err
	}

	return &Card{
		UUID:        uuid,
		ProjectUUID: projectUUID,
		LaneUUID:    laneUUID,
		Title:       title,
		Position:    position,
		Priority:    PriorityNone,
		CreatedBy:   createdBy,
		CreatedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
	}, nil
}

func validateTitle(title string) string {
	switch {
	case title == "":
		return "is required"
	case utf8.RuneCountInString(title) > MaxTitleLength:
		return "is too long"
	}
	return ""
}

// Edit changes every field the card form exposes. Lane and position are not
// among them: those only change through a move, so that ordering stays dense.
func (c *Card) Edit(
	title, description string,
	priority Priority,
	assigneeUUID *string,
	dueAt *time.Time,
	now time.Time,
) error {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)

	err := apperrors.Validation("that card is not valid")
	invalid := false

	if problem := validateTitle(title); problem != "" {
		err = err.WithField("title", problem)
		invalid = true
	}
	if utf8.RuneCountInString(description) > MaxDescriptionLength {
		err = err.WithField("description", "is too long")
		invalid = true
	}
	if !priority.Valid() {
		err = err.WithField("priority", "is not a known priority")
		invalid = true
	}
	if assigneeUUID != nil && *assigneeUUID == "" {
		err = err.WithField("assignee", "is not a known person")
		invalid = true
	}
	if invalid {
		return err
	}

	c.Title = title
	c.Description = description
	c.Priority = priority
	c.AssigneeUUID = assigneeUUID
	if dueAt == nil {
		c.DueAt = nil
	} else {
		due := dueAt.UTC()
		c.DueAt = &due
	}
	c.UpdatedAt = now.UTC()
	return nil
}

// Archive hides the card, keeping its history.
func (c *Card) Archive(now time.Time) {
	if c.ArchivedAt == nil {
		at := now.UTC()
		c.ArchivedAt = &at
		c.UpdatedAt = at
	}
}

// Restore brings an archived card back.
func (c *Card) Restore(now time.Time) {
	c.ArchivedAt = nil
	c.UpdatedAt = now.UTC()
}

// IsArchived reports whether the card is hidden.
func (c *Card) IsArchived() bool { return c.ArchivedAt != nil }

// IsOverdue reports whether the card has a due date in the past. A card in a
// lane marked done is never overdue, which is why the lane's state is a
// parameter rather than something this type guesses.
func (c *Card) IsOverdue(now time.Time, laneIsDone bool) bool {
	return c.DueAt != nil && !laneIsDone && c.DueAt.Before(now.UTC())
}
