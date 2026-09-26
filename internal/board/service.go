package board

import (
	"context"
	"log/slog"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// The failures this service reports.
var (
	// ErrNotManager is the wall. Every method that changes the shape of a board
	// returns this to a member, regardless of what the UI offered them.
	ErrNotManager = apperrors.Forbidden("You cannot change this board's settings.")

	// ErrTooManyLanes bounds the board.
	ErrTooManyLanes = apperrors.Conflict("This board already has as many lanes as it can hold.")

	// ErrNoSuchLane covers a lane that does not exist and one that belongs to
	// another board, for the same reason project lookups do: a 403 would confirm
	// it exists.
	ErrNoSuchLane = apperrors.NotFound("No such lane.")

	// ErrLaneHoldsCards refuses a delete that would lose work.
	ErrLaneHoldsCards = apperrors.Conflict(
		"That lane still holds cards. Choose a lane to move them to.")
)

// Service owns the shape of a board: its lanes and its labels.
//
// Every mutating method takes an authz.Subject and checks it first. The handler
// checks too, but this is what makes the rule structural — there is no way to call
// these without presenting a subject, so a new route cannot forget.
type Service struct {
	store *Store
	tx    *database.TxManager
	log   *slog.Logger
	now   func() time.Time
}

// NewService wires the service.
func NewService(store *Store, tx *database.TxManager, log *slog.Logger) *Service {
	return &Service{store: store, tx: tx, log: log, now: time.Now}
}

// WithClock replaces the clock. Tests only.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Lanes returns a board's lanes in display order. Any granted member may read.
func (s *Service) Lanes(ctx context.Context, subj authz.Subject, projectUUID string) ([]*domain.Lane, error) {
	if !subj.CanSeeProject() {
		return nil, apperrors.NotFound("No such project.")
	}
	return s.store.LanesForProject(ctx, projectUUID)
}

// Lane returns one lane, scoped to the board it must belong to.
//
// The projectUUID check is the authorisation: a lane uuid from another board
// resolves to nothing here, so a member of one project cannot read or edit another
// project's lane by naming its id.
func (s *Service) Lane(ctx context.Context, subj authz.Subject, projectUUID, laneUUID string) (*domain.Lane, error) {
	if !subj.CanSeeProject() {
		return nil, ErrNoSuchLane
	}
	lane, err := s.store.LaneByUUID(ctx, laneUUID)
	if err != nil {
		return nil, err
	}
	if lane == nil || lane.ProjectUUID != projectUUID {
		return nil, ErrNoSuchLane
	}
	return lane, nil
}

// LaneInput is a lane as a form submits it.
type LaneInput struct {
	Name   string
	Colour domain.Colour
	// WIPLimit is nil for "no limit", which is what an empty field means.
	WIPLimit *int
	IsDone   bool
}

// CreateLane adds a lane at the end of the board.
func (s *Service) CreateLane(
	ctx context.Context, subj authz.Subject, projectUUID string, in LaneInput,
) (*domain.Lane, error) {
	if !subj.CanManageBoard() {
		return nil, ErrNotManager
	}

	var lane *domain.Lane
	// The count, the position and the insert in one transaction: two concurrent
	// creates that each read the old maximum would otherwise both claim it, and
	// both pass a count check that only one of them should.
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		count, err := s.store.CountLanes(ctx, projectUUID)
		if err != nil {
			return err
		}
		if count >= domain.MaxLanesPerProject {
			return ErrTooManyLanes
		}

		position, err := s.store.NextLanePosition(ctx, projectUUID)
		if err != nil {
			return err
		}

		lane, err = domain.NewLane(
			idgenerator.NewUUIDv7(), projectUUID, in.Name, position,
			in.Colour, in.WIPLimit, in.IsDone, s.now())
		if err != nil {
			return err
		}
		return s.store.CreateLane(ctx, lane)
	})
	if err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "lane created", "project", projectUUID, "lane", lane.UUID)
	return lane, nil
}

// UpdateLane renames, recolours, and sets or clears the WIP limit.
//
// It does not move the lane: position changes only through ReorderLanes, so
// ordering stays dense.
func (s *Service) UpdateLane(
	ctx context.Context, subj authz.Subject, projectUUID, laneUUID string, in LaneInput,
) (*domain.Lane, error) {
	if !subj.CanManageBoard() {
		return nil, ErrNotManager
	}

	lane, err := s.Lane(ctx, subj, projectUUID, laneUUID)
	if err != nil {
		return nil, err
	}
	if err := lane.Update(in.Name, in.Colour, in.WIPLimit, in.IsDone, s.now()); err != nil {
		return nil, err
	}
	if err := s.store.UpdateLane(ctx, lane); err != nil {
		return nil, err
	}
	return lane, nil
}

// ReorderLanes rewrites the whole board's lane order.
//
// The client sends the order it is already displaying and the server agrees with
// it, exactly as card moves work. The order must name every lane on the board:
// a partial order would leave the rest with positions that collide with the new
// ones.
func (s *Service) ReorderLanes(
	ctx context.Context, subj authz.Subject, projectUUID string, order []string,
) error {
	if !subj.CanManageBoard() {
		return ErrNotManager
	}

	return s.tx.InTx(ctx, func(ctx context.Context) error {
		existing, err := s.store.LanesForProject(ctx, projectUUID)
		if err != nil {
			return err
		}
		if len(order) != len(existing) {
			return apperrors.Invalid(
				"That ordering does not match this board's lanes.")
		}
		return s.store.ReorderLanes(ctx, projectUUID, order, database.NewTime(s.now()))
	})
}

// DeleteLane removes a lane, moving any cards it holds into moveTo.
//
// An empty moveTo is only valid for an empty lane. There is no silent cascade: the
// cards foreign key is ON DELETE RESTRICT precisely so that losing work requires
// saying where it goes.
func (s *Service) DeleteLane(
	ctx context.Context, subj authz.Subject, projectUUID, laneUUID, moveTo string,
) error {
	if !subj.CanManageBoard() {
		return ErrNotManager
	}

	return s.tx.InTx(ctx, func(ctx context.Context) error {
		// Confirm the lane is on this board before touching anything, so a lane
		// uuid from elsewhere is a 404 rather than a store-level refusal.
		if _, err := s.Lane(ctx, subj, projectUUID, laneUUID); err != nil {
			return err
		}

		// A board with one lane is a list; deleting the last one leaves nowhere
		// for a card to exist.
		count, err := s.store.CountLanes(ctx, projectUUID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return apperrors.Conflict("A board needs at least one lane.")
		}

		if err := s.store.DeleteLane(ctx, projectUUID, laneUUID, moveTo); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "lane deleted",
			"project", projectUUID, "lane", laneUUID, "cards_moved_to", moveTo)
		return nil
	})
}

// Labels returns a board's labels. Any granted member may read.
func (s *Service) Labels(ctx context.Context, subj authz.Subject, projectUUID string) ([]*domain.Label, error) {
	if !subj.CanSeeProject() {
		return nil, apperrors.NotFound("No such project.")
	}
	return s.store.LabelsForProject(ctx, projectUUID)
}

// LabelInput is a label as a form submits it.
type LabelInput struct {
	Name   string
	Colour domain.Colour
}

// CreateLabel adds a project label. Manager or above.
func (s *Service) CreateLabel(
	ctx context.Context, subj authz.Subject, projectUUID string, in LabelInput,
) (*domain.Label, error) {
	if !subj.Can(authz.ActionLabelManage) {
		return nil, ErrNotManager
	}

	label, err := domain.NewLabel(idgenerator.NewUUIDv7(), projectUUID, in.Name, in.Colour, s.now())
	if err != nil {
		return nil, err
	}
	if err := s.store.CreateLabel(ctx, label); err != nil {
		return nil, err
	}
	return label, nil
}

// UpdateLabel renames and recolours a label. Manager or above.
func (s *Service) UpdateLabel(
	ctx context.Context, subj authz.Subject, projectUUID, labelUUID string, in LabelInput,
) (*domain.Label, error) {
	if !subj.Can(authz.ActionLabelManage) {
		return nil, ErrNotManager
	}

	label, err := s.store.LabelByUUID(ctx, labelUUID)
	if err != nil {
		return nil, err
	}
	if label == nil || label.ProjectUUID != projectUUID {
		return nil, apperrors.NotFound("No such label.")
	}
	if err := label.Rename(in.Name, in.Colour, s.now()); err != nil {
		return nil, err
	}
	if err := s.store.UpdateLabel(ctx, label); err != nil {
		return nil, err
	}
	return label, nil
}

// DeleteLabel removes a label. Its assignments to cards cascade. Manager or above.
func (s *Service) DeleteLabel(ctx context.Context, subj authz.Subject, projectUUID, labelUUID string) error {
	if !subj.Can(authz.ActionLabelManage) {
		return ErrNotManager
	}
	return s.store.DeleteLabel(ctx, projectUUID, labelUUID)
}

// SeedDefaultLanes gives a new board the five lanes the brief names, so a project
// is usable the moment it is created rather than an empty rail with no affordance.
//
// Called inside the project-creation transaction. The names are a starting point a
// manager renames, reorders or deletes.
func (s *Service) SeedDefaultLanes(ctx context.Context, projectUUID string) error {
	defaults := []struct {
		name   string
		colour domain.Colour
		isDone bool
	}{
		{"Backlog", domain.ColourSlate, false},
		{"In progress", domain.ColourBlue, false},
		{"Testing", domain.ColourAmber, false},
		{"Done", domain.ColourGreen, true},
		{"Stash", domain.ColourViolet, false},
	}

	now := s.now()
	for position, d := range defaults {
		lane, err := domain.NewLane(
			idgenerator.NewUUIDv7(), projectUUID, d.name, position,
			d.colour, nil, d.isDone, now)
		if err != nil {
			return err
		}
		if err := s.store.CreateLane(ctx, lane); err != nil {
			return err
		}
	}
	return nil
}

// ProjectOfLane returns the project a lane belongs to.
//
// Unscoped, for the same reason card.Service.ProjectOf is: `/lanes/{l}/fragment` is
// the revert target after a failed drag and carries no project in its path. The
// caller resolves access against this and then re-reads the lane scoped, so a lane
// from another board is a 404.
func (s *Service) ProjectOfLane(ctx context.Context, laneUUID string) (string, error) {
	lane, err := s.store.LaneByUUID(ctx, laneUUID)
	if err != nil {
		return "", err
	}
	if lane == nil {
		return "", ErrNoSuchLane
	}
	return lane.ProjectUUID, nil
}
