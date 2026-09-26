package card

import (
	"context"
	"log/slog"
	"time"

	boarddomain "github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/card/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// The failures this service reports.
var (
	// ErrNoSuchCard covers a card that does not exist and one that belongs to
	// another board, for the same reason project and lane lookups do: a 403 would
	// confirm it exists.
	ErrNoSuchCard = apperrors.NotFound("No such card.")

	// ErrNotYours is what a member gets for somebody else's card. Distinct from
	// ErrNoSuchCard because they can see the card — hiding its existence here
	// would be confusing rather than protective.
	ErrNotYours = apperrors.Forbidden("You can only delete your own cards.")
)

// BoardReader is what this slice needs from the board slice: the lanes a card can
// live in.
//
// Declared here, by the consumer. Card never queries the lanes table itself — when
// it needs to know whether a lane belongs to a project, it asks.
type BoardReader interface {
	Lanes(ctx context.Context, subj authz.Subject, projectUUID string) ([]*boarddomain.Lane, error)
	Lane(ctx context.Context, subj authz.Subject, projectUUID, laneUUID string) (*boarddomain.Lane, error)
}

// Service owns cards: creating them, editing them, and moving them.
//
// Every method takes an authz.Subject and checks it. Unlike the board slice, most of
// these are permitted to a member — creating, editing and dragging cards is what a
// member is here to do. Deleting somebody else's is not.
type Service struct {
	store *Store
	board BoardReader
	tx    *database.TxManager
	log   *slog.Logger
	now   func() time.Time
}

// NewService wires the service.
func NewService(store *Store, board BoardReader, tx *database.TxManager, log *slog.Logger) *Service {
	return &Service{store: store, board: board, tx: tx, log: log, now: time.Now}
}

// WithClock replaces the clock. Tests only.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// ForProject returns every live card on a board, grouped by lane uuid.
//
// One query for the whole board rather than one per lane: a board with six lanes
// would otherwise be six round trips to render one page.
func (s *Service) ForProject(
	ctx context.Context, subj authz.Subject, projectUUID string,
) (map[string][]*domain.Card, error) {
	if !subj.CanSeeProject() {
		return nil, apperrors.NotFound("No such project.")
	}

	cards, err := s.store.ForProject(ctx, projectUUID)
	if err != nil {
		return nil, err
	}

	byLane := make(map[string][]*domain.Card)
	for _, c := range cards {
		byLane[c.LaneUUID] = append(byLane[c.LaneUUID], c)
	}
	return byLane, nil
}

// ForLane returns one lane's live cards in order, which is what a lane fragment
// needs after a move.
func (s *Service) ForLane(
	ctx context.Context, subj authz.Subject, projectUUID, laneUUID string,
) (*boarddomain.Lane, []*domain.Card, error) {
	// Resolving the lane through the board slice is the authorisation: a lane from
	// another project does not resolve, so its cards are unreachable.
	lane, err := s.board.Lane(ctx, subj, projectUUID, laneUUID)
	if err != nil {
		return nil, nil, err
	}

	cards, err := s.store.ForLane(ctx, laneUUID)
	if err != nil {
		return nil, nil, err
	}
	return lane, cards, nil
}

// ByUUID returns one card, scoped to the board it must belong to.
func (s *Service) ByUUID(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string,
) (*domain.Card, error) {
	if !subj.CanSeeProject() {
		return nil, ErrNoSuchCard
	}

	card, err := s.store.ByUUID(ctx, cardUUID)
	if err != nil {
		return nil, err
	}
	if card == nil || card.ProjectUUID != projectUUID {
		return nil, ErrNoSuchCard
	}
	return card, nil
}

// Create adds a card at the end of a lane.
func (s *Service) Create(
	ctx context.Context, subj authz.Subject, projectUUID, laneUUID, title string,
) (*domain.Card, error) {
	if !subj.Can(authz.ActionCardCreate) {
		return nil, apperrors.Forbidden("You cannot add cards to this board.")
	}

	// Through the board slice, so a lane uuid from another project is refused
	// before anything is written.
	if _, err := s.board.Lane(ctx, subj, projectUUID, laneUUID); err != nil {
		return nil, err
	}

	var card *domain.Card
	// The position and the insert in one transaction: two concurrent creates that
	// each read the old maximum would otherwise both claim it.
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		position, err := s.store.NextPosition(ctx, laneUUID)
		if err != nil {
			return err
		}

		card, err = domain.NewCard(
			idgenerator.NewUUIDv7(), projectUUID, laneUUID, title,
			subj.User.UUID, position, s.now())
		if err != nil {
			return err
		}
		if err := s.store.Create(ctx, card); err != nil {
			return err
		}
		return s.record(ctx, card, ActivityCreated, nil, &laneUUID, subj.User.UUID)
	})
	if err != nil {
		return nil, err
	}
	return card, nil
}

// EditInput is a card as the edit form submits it.
//
// Title and description only, for now. Priority, assignee, due date and labels are
// phase 5; the domain entity and the store already carry them, so this struct grows
// rather than the plumbing around it.
type EditInput struct {
	Title       string
	Description string
}

// Edit saves a card's fields. Any granted member may edit any card on their board —
// the brief is explicit about that, and it is what makes a shared board work.
func (s *Service) Edit(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string, in EditInput,
) (*domain.Card, error) {
	if !subj.Can(authz.ActionCardEdit) {
		return nil, apperrors.Forbidden("You cannot edit cards on this board.")
	}

	card, err := s.ByUUID(ctx, subj, projectUUID, cardUUID)
	if err != nil {
		return nil, err
	}

	// The phase-5 fields are passed back unchanged, so editing a title cannot
	// silently clear a due date set elsewhere.
	if err := card.Edit(
		in.Title, in.Description, card.Priority,
		card.AssigneeUUID, card.DueAt, s.now(),
	); err != nil {
		return nil, err
	}

	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Update(ctx, card); err != nil {
			return err
		}
		return s.record(ctx, card, ActivityUpdated, nil, nil, subj.User.UUID)
	})
	if err != nil {
		return nil, err
	}
	return card, nil
}

// SetArchived archives or restores a card.
//
// Archiving is the reversible way to get a card off the board, and is what the UI
// offers before delete.
func (s *Service) SetArchived(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string, archived bool,
) (*domain.Card, error) {
	if !subj.Can(authz.ActionCardArchive) {
		return nil, apperrors.Forbidden("You cannot archive cards on this board.")
	}

	card, err := s.ByUUID(ctx, subj, projectUUID, cardUUID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	kind := ActivityRestored
	if archived {
		card.Archive(now)
		kind = ActivityArchived
	} else {
		card.Restore(now)
	}

	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Update(ctx, card); err != nil {
			return err
		}
		// Archiving takes the card out of the lane's live sequence, and restoring
		// puts it back at whatever position it held. Either way the lane needs
		// renumbering to stay dense.
		if err := s.store.CompactLane(ctx, card.LaneUUID, now); err != nil {
			return err
		}
		return s.record(ctx, card, kind, nil, nil, subj.User.UUID)
	})
	if err != nil {
		return nil, err
	}
	return card, nil
}

// Delete removes a card permanently. Comments, attachments, labels and activity go
// with it.
//
// A member may delete only their own; a manager may delete any card on their board.
// That is the one card operation where ownership matters, which is why it goes
// through CanOn.
func (s *Service) Delete(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string,
) (*domain.Card, error) {
	card, err := s.ByUUID(ctx, subj, projectUUID, cardUUID)
	if err != nil {
		return nil, err
	}

	if !subj.CanOn(authz.ActionCardDelete, card.CreatedBy) {
		return nil, ErrNotYours
	}

	// The delete and the renumbering together, so the lane is never observed with
	// a hole in it.
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Delete(ctx, projectUUID, cardUUID); err != nil {
			return err
		}
		return s.store.CompactLane(ctx, card.LaneUUID, s.now())
	})
	if err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "card deleted",
		"project", projectUUID, "card", cardUUID, "by", subj.User.UUID)
	return card, nil
}

// Move applies a drag and returns the lanes whose contents changed.
//
// The snapshot, the plan, every renumbered row and the activity entry are all in one
// transaction. That is what makes a WIP breach or a foreign card a clean refusal
// rather than a half-applied move, and it is why the activity log cannot disagree
// with the board.
func (s *Service) Move(
	ctx context.Context, subj authz.Subject, projectUUID string, m domain.Move,
) ([]string, error) {
	if !subj.Can(authz.ActionCardMove) {
		return nil, apperrors.Forbidden("You cannot move cards on this board.")
	}

	var affected []string
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		// Read before the move, so the activity row can say where the card came
		// from. Inside the transaction, so it is the same state the plan is made
		// against.
		before, err := s.store.ByUUID(ctx, m.CardUUID)
		if err != nil {
			return err
		}
		if before == nil || before.ProjectUUID != projectUUID {
			return domain.ErrCardNotOnBoard
		}

		affected, err = s.store.Move(ctx, projectUUID, m, s.now())
		if err != nil {
			return err
		}

		// A lane the card left is affected even when no placement was written for
		// it. Dragging the last card out leaves an empty source order, so the plan
		// renumbers nothing there — but the lane did change, and a tab that is not
		// told would go on showing the card in it.
		if before.LaneUUID != m.ToLane && !containsLane(affected, before.LaneUUID) {
			affected = append(affected, before.LaneUUID)
		}

		// Only a lane change is worth an activity row. A reorder inside one lane
		// happens constantly and would drown the history of a card in noise.
		if before.LaneUUID == m.ToLane {
			return nil
		}
		from := before.LaneUUID
		to := m.ToLane
		return s.record(ctx, before, ActivityMoved, &from, &to, subj.User.UUID)
	})
	if err != nil {
		return nil, err
	}
	return affected, nil
}

// History returns a card's activity, newest first.
func (s *Service) History(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string, limit int,
) ([]Activity, error) {
	if _, err := s.ByUUID(ctx, subj, projectUUID, cardUUID); err != nil {
		return nil, err
	}
	return s.store.ActivityForCard(ctx, cardUUID, limit)
}

// containsLane reports whether a lane is already in the affected list.
func containsLane(lanes []string, lane string) bool {
	for _, l := range lanes {
		if l == lane {
			return true
		}
	}
	return false
}

// record writes one activity row. Always called inside the transaction that made
// the change, so history cannot disagree with the data.
func (s *Service) record(
	ctx context.Context, card *domain.Card, kind ActivityKind,
	fromLane, toLane *string, actorUUID string,
) error {
	return s.store.RecordActivity(ctx, Activity{
		UUID:         idgenerator.NewUUIDv7(),
		CardUUID:     card.UUID,
		ProjectUUID:  card.ProjectUUID,
		ActorUUID:    actorUUID,
		Kind:         kind,
		FromLaneUUID: fromLane,
		ToLaneUUID:   toLane,
		CreatedAt:    s.now(),
	})
}

// ProjectOf returns the project a card belongs to.
//
// Deliberately unscoped: it is the lookup that *enables* scoping. Card URLs are
// `/cards/{c}` — short, because they appear in the DOM on every card and in the move
// request board.js builds — so there is no project in the path to resolve access
// from. The caller resolves access against the uuid this returns and then re-reads
// the card scoped to it, so an ungranted card is a 404 exactly as it would be under
// a project-scoped URL.
func (s *Service) ProjectOf(ctx context.Context, cardUUID string) (string, error) {
	card, err := s.store.ByUUID(ctx, cardUUID)
	if err != nil {
		return "", err
	}
	if card == nil {
		return "", ErrNoSuchCard
	}
	return card.ProjectUUID, nil
}
