// Package domain holds the rules about the shape of a board: its lanes and its
// labels. Only a project manager or the operator may change anything here, which
// is enforced at the route rather than by hiding a control.
package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Colour is one of the palette tokens in web/css/input.css. It is a closed set
// because a lane colour has to match a Tailwind class that exists at build time:
// a free-form hex would produce an unstyled lane, and Tailwind cannot generate a
// class from a runtime string.
type Colour string

// The palette, matching the --color-lane-* tokens and the schema's CHECK.
const (
	ColourSlate  Colour = "slate"
	ColourBlue   Colour = "blue"
	ColourTeal   Colour = "teal"
	ColourGreen  Colour = "green"
	ColourAmber  Colour = "amber"
	ColourRose   Colour = "rose"
	ColourViolet Colour = "violet"
)

// Colours is the palette in display order, for a colour picker.
var Colours = []Colour{
	ColourSlate, ColourBlue, ColourTeal, ColourGreen,
	ColourAmber, ColourRose, ColourViolet,
}

// Valid reports whether c is in the palette.
func (c Colour) Valid() bool {
	for _, known := range Colours {
		if c == known {
			return true
		}
	}
	return false
}

// Field bounds.
const (
	MaxLaneNameLength  = 60
	MaxLabelNameLength = 40
	// MaxLanesPerProject keeps the board scrollable rather than absurd, and
	// bounds the cost of the whole-board re-render a structural change causes.
	MaxLanesPerProject = 20
)

// Lane is one column of a board.
type Lane struct {
	UUID        string
	ProjectUUID string
	Name        string
	Position    int
	Colour      Colour
	// WIPLimit caps how many cards the lane may hold. nil means no limit; the
	// schema's CHECK refuses zero, because a lane nothing can enter is a
	// mistake rather than a policy.
	WIPLimit  *int
	IsDone    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewLane validates and builds a lane.
func NewLane(uuid, projectUUID, name string, position int, colour Colour, wipLimit *int, isDone bool, now time.Time) (*Lane, error) {
	name = strings.TrimSpace(name)

	err := apperrors.Validation("that lane is not valid")
	invalid := false

	if uuid == "" {
		err = err.WithField("uuid", "is required")
		invalid = true
	}
	if projectUUID == "" {
		err = err.WithField("project_uuid", "is required")
		invalid = true
	}
	if problem := validateLaneName(name); problem != "" {
		err = err.WithField("name", problem)
		invalid = true
	}
	if position < 0 {
		err = err.WithField("position", "must not be negative")
		invalid = true
	}
	if !colour.Valid() {
		err = err.WithField("colour", "is not in the palette")
		invalid = true
	}
	if wipLimit != nil && *wipLimit < 1 {
		err = err.WithField("wip_limit", "must be at least one, or empty for no limit")
		invalid = true
	}
	if invalid {
		return nil, err
	}

	return &Lane{
		UUID:        uuid,
		ProjectUUID: projectUUID,
		Name:        name,
		Position:    position,
		Colour:      colour,
		WIPLimit:    wipLimit,
		IsDone:      isDone,
		CreatedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
	}, nil
}

func validateLaneName(name string) string {
	switch {
	case name == "":
		return "is required"
	case utf8.RuneCountInString(name) > MaxLaneNameLength:
		return "is too long"
	}
	return ""
}

// Update changes everything a manager may edit about a lane except its position,
// which moves through a reorder so that the whole board stays dense.
func (l *Lane) Update(name string, colour Colour, wipLimit *int, isDone bool, now time.Time) error {
	name = strings.TrimSpace(name)

	err := apperrors.Validation("that lane is not valid")
	invalid := false
	if problem := validateLaneName(name); problem != "" {
		err = err.WithField("name", problem)
		invalid = true
	}
	if !colour.Valid() {
		err = err.WithField("colour", "is not in the palette")
		invalid = true
	}
	if wipLimit != nil && *wipLimit < 1 {
		err = err.WithField("wip_limit", "must be at least one, or empty for no limit")
		invalid = true
	}
	if invalid {
		return err
	}

	l.Name = name
	l.Colour = colour
	l.WIPLimit = wipLimit
	l.IsDone = isDone
	l.UpdatedAt = now.UTC()
	return nil
}

// Limit returns the WIP limit in the form the move planner wants, where zero
// means unlimited.
func (l *Lane) Limit() int {
	if l.WIPLimit == nil {
		return 0
	}
	return *l.WIPLimit
}

// Label is a tag that can be put on a card. Labels belong to a project, so two
// projects may both have a "bug" without sharing one.
type Label struct {
	UUID        string
	ProjectUUID string
	Name        string
	Colour      Colour
	CreatedAt   time.Time
}

// NewLabel validates and builds a label.
func NewLabel(uuid, projectUUID, name string, colour Colour, now time.Time) (*Label, error) {
	name = strings.TrimSpace(name)

	err := apperrors.Validation("that label is not valid")
	invalid := false

	if uuid == "" {
		err = err.WithField("uuid", "is required")
		invalid = true
	}
	if projectUUID == "" {
		err = err.WithField("project_uuid", "is required")
		invalid = true
	}
	switch {
	case name == "":
		err = err.WithField("name", "is required")
		invalid = true
	case utf8.RuneCountInString(name) > MaxLabelNameLength:
		err = err.WithField("name", "is too long")
		invalid = true
	}
	if !colour.Valid() {
		err = err.WithField("colour", "is not in the palette")
		invalid = true
	}
	if invalid {
		return nil, err
	}

	return &Label{
		UUID:        uuid,
		ProjectUUID: projectUUID,
		Name:        name,
		Colour:      colour,
		CreatedAt:   now.UTC(),
	}, nil
}

// Rename changes a label's text and colour.
func (l *Label) Rename(name string, colour Colour, now time.Time) error {
	renamed, err := NewLabel(l.UUID, l.ProjectUUID, name, colour, now)
	if err != nil {
		return err
	}
	l.Name = renamed.Name
	l.Colour = renamed.Colour
	return nil
}

// Reorder returns dense zero-based positions for lanes in the given order.
//
// The same discipline as card positions: a reorder renumbers the whole set
// inside one transaction rather than nudging neighbours, so there is never a gap
// or a tie to reconcile later.
func Reorder(order []string) (map[string]int, error) {
	if len(order) == 0 {
		return nil, apperrors.Invalid("the new order is empty")
	}
	positions := make(map[string]int, len(order))
	for i, uuid := range order {
		if uuid == "" {
			return nil, apperrors.Invalid("the new order contains an empty lane")
		}
		if _, duplicate := positions[uuid]; duplicate {
			return nil, apperrors.Invalid("the new order lists the same lane twice")
		}
		positions[uuid] = i
	}
	return positions, nil
}
