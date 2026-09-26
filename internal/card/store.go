package card

import (
	"context"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/card/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Store reads and writes the cards table, its label assignments, and its
// activity log.
type Store struct {
	tx *database.TxManager
}

// NewStore wires a store to the transaction manager.
func NewStore(tx *database.TxManager) *Store { return &Store{tx: tx} }

const cardColumns = `
	uuid, project_uuid, lane_uuid, title, description, position, priority,
	assignee_uuid, due_at, created_by, created_at, updated_at, archived_at`

type cardModel struct {
	UUID         string            `db:"uuid"`
	ProjectUUID  string            `db:"project_uuid"`
	LaneUUID     string            `db:"lane_uuid"`
	Title        string            `db:"title"`
	Description  string            `db:"description"`
	Position     int               `db:"position"`
	Priority     string            `db:"priority"`
	AssigneeUUID *string           `db:"assignee_uuid"`
	DueAt        database.NullTime `db:"due_at"`
	CreatedBy    string            `db:"created_by"`
	CreatedAt    database.Time     `db:"created_at"`
	UpdatedAt    database.Time     `db:"updated_at"`
	ArchivedAt   database.NullTime `db:"archived_at"`
}

func (m cardModel) toDomain() *domain.Card {
	return &domain.Card{
		UUID:         m.UUID,
		ProjectUUID:  m.ProjectUUID,
		LaneUUID:     m.LaneUUID,
		Title:        m.Title,
		Description:  m.Description,
		Position:     m.Position,
		Priority:     domain.Priority(m.Priority),
		AssigneeUUID: m.AssigneeUUID,
		DueAt:        m.DueAt.Ptr(),
		CreatedBy:    m.CreatedBy,
		CreatedAt:    m.CreatedAt.Time(),
		UpdatedAt:    m.UpdatedAt.Time(),
		ArchivedAt:   m.ArchivedAt.Ptr(),
	}
}

func toCardModel(c *domain.Card) cardModel {
	return cardModel{
		UUID:         c.UUID,
		ProjectUUID:  c.ProjectUUID,
		LaneUUID:     c.LaneUUID,
		Title:        c.Title,
		Description:  c.Description,
		Position:     c.Position,
		Priority:     string(c.Priority),
		AssigneeUUID: c.AssigneeUUID,
		DueAt:        database.NullTimeFrom(c.DueAt),
		CreatedBy:    c.CreatedBy,
		CreatedAt:    database.NewTime(c.CreatedAt),
		UpdatedAt:    database.NewTime(c.UpdatedAt),
		ArchivedAt:   database.NullTimeFrom(c.ArchivedAt),
	}
}

func toDomainCards(models []cardModel) []*domain.Card {
	out := make([]*domain.Card, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out
}

// Create inserts a card.
func (s *Store) Create(ctx context.Context, c *domain.Card) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO cards (`+cardColumns+`)
		VALUES (:uuid, :project_uuid, :lane_uuid, :title, :description, :position,
		        :priority, :assignee_uuid, :due_at, :created_by,
		        :created_at, :updated_at, :archived_at)`,
		toCardModel(c))
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid(
				"that lane, project or person no longer exists").Wrap(err)
		}
		return database.MapError(err, "create card")
	}
	return nil
}

// NextPosition returns the position a new card should take at the end of a lane.
// It belongs inside the same transaction as the insert: two concurrent creates
// that each read the old maximum would otherwise both claim it.
func (s *Store) NextPosition(ctx context.Context, laneUUID string) (int, error) {
	var next int
	if _, err := database.GetNamed(ctx, s.tx.Reader(ctx), &next, `
		SELECT coalesce(max(position) + 1, 0) FROM cards WHERE lane_uuid = :lane_uuid`,
		map[string]any{"lane_uuid": laneUUID}); err != nil {
		return 0, err
	}
	return next, nil
}

// ByUUID returns the card, or (nil, nil) when there is none.
func (s *Store) ByUUID(ctx context.Context, uuid string) (*domain.Card, error) {
	var m cardModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m,
		`SELECT `+cardColumns+` FROM cards WHERE uuid = :uuid`,
		map[string]any{"uuid": uuid})
	if err != nil || !found {
		return nil, err
	}
	return m.toDomain(), nil
}

// ForProject returns every live card on a board, ordered so that a caller can
// group them into lanes in one pass.
func (s *Store) ForProject(ctx context.Context, projectUUID string) ([]*domain.Card, error) {
	var models []cardModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT `+cardColumns+` FROM cards
		WHERE project_uuid = :project_uuid AND archived_at IS NULL
		ORDER BY lane_uuid, position, uuid`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return nil, err
	}
	return toDomainCards(models), nil
}

// ForLane returns one lane's live cards in order, which is what a lane fragment
// needs after a move.
func (s *Store) ForLane(ctx context.Context, laneUUID string) ([]*domain.Card, error) {
	var models []cardModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT `+cardColumns+` FROM cards
		WHERE lane_uuid = :lane_uuid AND archived_at IS NULL
		ORDER BY position, uuid`,
		map[string]any{"lane_uuid": laneUUID}); err != nil {
		return nil, err
	}
	return toDomainCards(models), nil
}

// Update writes back the fields the card form edits. It deliberately does not
// touch lane_uuid or position: those change only through Move, so that ordering
// stays dense.
func (s *Store) Update(ctx context.Context, c *domain.Card) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE cards SET
			title         = :title,
			description   = :description,
			priority      = :priority,
			assignee_uuid = :assignee_uuid,
			due_at        = :due_at,
			updated_at    = :updated_at,
			archived_at   = :archived_at
		WHERE uuid = :uuid`,
		toCardModel(c))
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid("that person no longer exists").Wrap(err)
		}
		return database.MapError(err, "update card")
	}
	return database.RequireRow(n, "that card no longer exists")
}

// Delete removes a card. Comments, attachments, label assignments and activity
// all cascade with it.
func (s *Store) Delete(ctx context.Context, projectUUID, uuid string) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		DELETE FROM cards WHERE uuid = :uuid AND project_uuid = :project_uuid`,
		map[string]any{"uuid": uuid, "project_uuid": projectUUID})
	if err != nil {
		return database.MapError(err, "delete card")
	}
	return database.RequireRow(n, "that card no longer exists")
}

// CompactLane renumbers a lane's live cards to 0..n-1.
//
// Removing a card — by delete or by archive — leaves a hole in the sequence. Nothing
// reads positions as anything but an ordering, so a gap is inert, but the invariant
// for this column is "dense and zero-based", and an invariant that holds except after
// a delete is a worse thing to have to remember than one that always holds.
//
// Ordered by position then uuid, so the result is deterministic even if two cards
// somehow share a position.
func (s *Store) CompactLane(ctx context.Context, laneUUID string, at time.Time) error {
	var uuids []string
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &uuids, `
		SELECT uuid FROM cards
		WHERE lane_uuid = :lane_uuid AND archived_at IS NULL
		ORDER BY position, uuid`,
		map[string]any{"lane_uuid": laneUUID}); err != nil {
		return err
	}

	ts := database.NewTime(at)
	for position, uuid := range uuids {
		if _, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
			UPDATE cards SET position = :position, updated_at = :at
			WHERE uuid = :uuid AND position <> :position`,
			map[string]any{"position": position, "at": ts, "uuid": uuid}); err != nil {
			return database.MapError(err, "compact lane")
		}
	}
	return nil
}

// Snapshot loads the board state the move planner needs: which lane every card
// is in, and each lane's WIP limit.
//
// It is read inside the move's own transaction, so the plan is made against the
// same state the write applies to. Reading it outside would leave a window in
// which another person's move invalidates the plan between check and write.
func (s *Store) Snapshot(ctx context.Context, projectUUID string) (domain.Board, error) {
	type laneRow struct {
		UUID     string `db:"uuid"`
		WIPLimit *int   `db:"wip_limit"`
	}
	var lanes []laneRow
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &lanes, `
		SELECT uuid, wip_limit FROM lanes WHERE project_uuid = :project_uuid`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return domain.Board{}, err
	}

	type cardRow struct {
		UUID     string `db:"uuid"`
		LaneUUID string `db:"lane_uuid"`
	}
	var cards []cardRow
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &cards, `
		SELECT uuid, lane_uuid FROM cards
		WHERE project_uuid = :project_uuid AND archived_at IS NULL`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return domain.Board{}, err
	}

	board := domain.Board{
		Lanes:      make(map[string]domain.Lane, len(lanes)),
		LaneOfCard: make(map[string]string, len(cards)),
	}
	for _, l := range lanes {
		limit := 0
		if l.WIPLimit != nil {
			limit = *l.WIPLimit
		}
		board.Lanes[l.UUID] = domain.Lane{UUID: l.UUID, WIPLimit: limit}
	}
	for _, c := range cards {
		board.LaneOfCard[c.UUID] = c.LaneUUID
	}
	return board, nil
}

// Move validates a drag against the board and applies it, returning the lanes
// whose contents changed so the caller can broadcast them.
//
// The whole operation is one transaction: the snapshot, the plan, and every
// renumbered row. That is what makes a WIP breach or a foreign card a clean
// refusal rather than a half-applied move, and it is why (lane_uuid, position)
// is indexed but not unique — the intermediate states inside this loop have
// duplicate positions by design.
func (s *Store) Move(ctx context.Context, projectUUID string, m domain.Move, at time.Time) ([]string, error) {
	var affected []string

	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		board, err := s.Snapshot(ctx, projectUUID)
		if err != nil {
			return err
		}

		placements, err := domain.Plan(board, m)
		if err != nil {
			return err
		}

		ts := database.NewTime(at)
		for _, p := range placements {
			// Scoping each update to the project is the authorisation check:
			// a card uuid from another board matches nothing, and the row
			// count turns that into a refusal rather than a silent no-op.
			n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
				UPDATE cards SET
					lane_uuid  = :lane_uuid,
					position   = :position,
					updated_at = :at
				WHERE uuid = :uuid AND project_uuid = :project_uuid`,
				map[string]any{
					"lane_uuid": p.LaneUUID, "position": p.Position, "at": ts,
					"uuid": p.CardUUID, "project_uuid": projectUUID,
				})
			if err != nil {
				return database.MapError(err, "move card")
			}
			if n == 0 {
				return domain.ErrCardNotOnBoard
			}
		}

		affected = domain.AffectedLanes(placements)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return affected, nil
}

// SetLabels replaces a card's labels with exactly the given set.
//
// Delete-then-insert rather than a diff: the set is tiny, the whole thing is one
// transaction, and computing a minimal change would be more code with more ways
// to be subtly wrong.
func (s *Store) SetLabels(ctx context.Context, cardUUID string, labelUUIDs []string) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := database.ExecNamed(ctx, s.tx.Writer(ctx),
			`DELETE FROM card_labels WHERE card_uuid = :card_uuid`,
			map[string]any{"card_uuid": cardUUID}); err != nil {
			return database.MapError(err, "clear card labels")
		}

		for _, labelUUID := range labelUUIDs {
			// The label must belong to the same project as the card. Checking
			// it in the INSERT ... SELECT means one statement decides, and a
			// label from another board simply inserts nothing.
			n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
				INSERT INTO card_labels (card_uuid, label_uuid)
				SELECT c.uuid, l.uuid
				FROM cards c
				JOIN labels l ON l.project_uuid = c.project_uuid
				WHERE c.uuid = :card_uuid AND l.uuid = :label_uuid`,
				map[string]any{"card_uuid": cardUUID, "label_uuid": labelUUID})
			if err != nil {
				return database.MapError(err, "add card label")
			}
			if n == 0 {
				return apperrors.NotFound("that label is not on this board")
			}
		}
		return nil
	})
}

// LabelUUIDsForCards returns the label assignments for a set of cards, keyed by
// card. One query for a whole board rather than one per card.
func (s *Store) LabelUUIDsForCards(ctx context.Context, cardUUIDs []string) (map[string][]string, error) {
	if len(cardUUIDs) == 0 {
		return map[string][]string{}, nil
	}

	type row struct {
		CardUUID  string `db:"card_uuid"`
		LabelUUID string `db:"label_uuid"`
	}
	var rows []row
	// A named parameter cannot hold a slice, so the IN list is expanded before
	// it is rebound. This is the one place a query is written with a positional
	// placeholder.
	if err := database.SelectIn(ctx, s.tx.Reader(ctx), &rows, `
		SELECT cl.card_uuid, cl.label_uuid
		FROM card_labels cl
		JOIN labels l ON l.uuid = cl.label_uuid
		WHERE cl.card_uuid IN (?)
		ORDER BY l.name, l.uuid`,
		cardUUIDs); err != nil {
		return nil, err
	}

	out := make(map[string][]string, len(cardUUIDs))
	for _, r := range rows {
		out[r.CardUUID] = append(out[r.CardUUID], r.LabelUUID)
	}
	return out, nil
}

// ActivityKind names something that happened to a card. The set matches the
// schema's CHECK.
type ActivityKind string

// The activity kinds.
const (
	ActivityCreated   ActivityKind = "created"
	ActivityMoved     ActivityKind = "moved"
	ActivityUpdated   ActivityKind = "updated"
	ActivityArchived  ActivityKind = "archived"
	ActivityRestored  ActivityKind = "restored"
	ActivityCommented ActivityKind = "commented"
	ActivityAttached  ActivityKind = "attached"
	ActivityAssigned  ActivityKind = "assigned"
	ActivityLabelled  ActivityKind = "labelled"
)

// Activity is one entry in a card's history.
type Activity struct {
	UUID         string
	CardUUID     string
	ProjectUUID  string
	ActorUUID    string
	Kind         ActivityKind
	FromLaneUUID *string
	ToLaneUUID   *string
	Meta         string
	CreatedAt    time.Time
}

// RecordActivity appends to a card's history. Called inside the transaction that
// made the change, so history cannot disagree with the data.
func (s *Store) RecordActivity(ctx context.Context, a Activity) error {
	meta := a.Meta
	if meta == "" {
		meta = "{}"
	}
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO card_activity (uuid, card_uuid, project_uuid, actor_uuid, kind,
		                           from_lane_uuid, to_lane_uuid, meta, created_at)
		VALUES (:uuid, :card_uuid, :project_uuid, :actor_uuid, :kind,
		        :from_lane_uuid, :to_lane_uuid, :meta, :created_at)`,
		map[string]any{
			"uuid": a.UUID, "card_uuid": a.CardUUID, "project_uuid": a.ProjectUUID,
			"actor_uuid": a.ActorUUID, "kind": string(a.Kind),
			"from_lane_uuid": a.FromLaneUUID, "to_lane_uuid": a.ToLaneUUID,
			"meta": meta, "created_at": database.NewTime(a.CreatedAt),
		})
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid("that card or person no longer exists").Wrap(err)
		}
		return database.MapError(err, "record activity")
	}
	return nil
}

// ActivityForCard returns a card's history, newest first.
func (s *Store) ActivityForCard(ctx context.Context, cardUUID string, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}

	type activityModel struct {
		UUID         string        `db:"uuid"`
		CardUUID     string        `db:"card_uuid"`
		ProjectUUID  string        `db:"project_uuid"`
		ActorUUID    string        `db:"actor_uuid"`
		Kind         string        `db:"kind"`
		FromLaneUUID *string       `db:"from_lane_uuid"`
		ToLaneUUID   *string       `db:"to_lane_uuid"`
		Meta         string        `db:"meta"`
		CreatedAt    database.Time `db:"created_at"`
	}

	var models []activityModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT uuid, card_uuid, project_uuid, actor_uuid, kind,
		       from_lane_uuid, to_lane_uuid, meta, created_at
		FROM card_activity
		WHERE card_uuid = :card_uuid
		ORDER BY created_at DESC, uuid DESC
		LIMIT :limit`,
		map[string]any{"card_uuid": cardUUID, "limit": limit}); err != nil {
		return nil, err
	}

	out := make([]Activity, 0, len(models))
	for _, m := range models {
		out = append(out, Activity{
			UUID: m.UUID, CardUUID: m.CardUUID, ProjectUUID: m.ProjectUUID,
			ActorUUID: m.ActorUUID, Kind: ActivityKind(m.Kind),
			FromLaneUUID: m.FromLaneUUID, ToLaneUUID: m.ToLaneUUID,
			Meta: m.Meta, CreatedAt: m.CreatedAt.Time(),
		})
	}
	return out, nil
}
