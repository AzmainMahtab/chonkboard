package board

import (
	"context"

	"github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Store reads and writes the lanes and labels tables — the shape of a board.
type Store struct {
	tx *database.TxManager
}

// NewStore wires a store to the transaction manager.
func NewStore(tx *database.TxManager) *Store { return &Store{tx: tx} }

const laneColumns = `
	uuid, project_uuid, name, position, color, wip_limit, is_done,
	created_at, updated_at`

type laneModel struct {
	UUID        string        `db:"uuid"`
	ProjectUUID string        `db:"project_uuid"`
	Name        string        `db:"name"`
	Position    int           `db:"position"`
	Colour      string        `db:"color"`
	WIPLimit    *int          `db:"wip_limit"`
	IsDone      bool          `db:"is_done"`
	CreatedAt   database.Time `db:"created_at"`
	UpdatedAt   database.Time `db:"updated_at"`
}

func (m laneModel) toDomain() *domain.Lane {
	return &domain.Lane{
		UUID:        m.UUID,
		ProjectUUID: m.ProjectUUID,
		Name:        m.Name,
		Position:    m.Position,
		Colour:      domain.Colour(m.Colour),
		WIPLimit:    m.WIPLimit,
		IsDone:      m.IsDone,
		CreatedAt:   m.CreatedAt.Time(),
		UpdatedAt:   m.UpdatedAt.Time(),
	}
}

func toLaneModel(l *domain.Lane) laneModel {
	return laneModel{
		UUID:        l.UUID,
		ProjectUUID: l.ProjectUUID,
		Name:        l.Name,
		Position:    l.Position,
		Colour:      string(l.Colour),
		WIPLimit:    l.WIPLimit,
		IsDone:      l.IsDone,
		CreatedAt:   database.NewTime(l.CreatedAt),
		UpdatedAt:   database.NewTime(l.UpdatedAt),
	}
}

// CreateLane inserts a lane at the end of the board.
func (s *Store) CreateLane(ctx context.Context, l *domain.Lane) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO lanes (`+laneColumns+`)
		VALUES (:uuid, :project_uuid, :name, :position, :color, :wip_limit,
		        :is_done, :created_at, :updated_at)`,
		toLaneModel(l))
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid("that project no longer exists").Wrap(err)
		}
		return database.MapError(err, "create lane")
	}
	return nil
}

// NextLanePosition returns the position a new lane should take. Callers run it
// inside the same transaction as the insert, because two concurrent creates that
// both read the old maximum would otherwise collide on a position.
func (s *Store) NextLanePosition(ctx context.Context, projectUUID string) (int, error) {
	var next int
	if _, err := database.GetNamed(ctx, s.tx.Reader(ctx), &next, `
		SELECT coalesce(max(position) + 1, 0) FROM lanes
		WHERE project_uuid = :project_uuid`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return 0, err
	}
	return next, nil
}

// LaneByUUID returns the lane, or (nil, nil) when there is none.
func (s *Store) LaneByUUID(ctx context.Context, uuid string) (*domain.Lane, error) {
	var m laneModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m,
		`SELECT `+laneColumns+` FROM lanes WHERE uuid = :uuid`,
		map[string]any{"uuid": uuid})
	if err != nil || !found {
		return nil, err
	}
	return m.toDomain(), nil
}

// LanesForProject returns the board's lanes in display order.
func (s *Store) LanesForProject(ctx context.Context, projectUUID string) ([]*domain.Lane, error) {
	var models []laneModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT `+laneColumns+` FROM lanes
		WHERE project_uuid = :project_uuid
		ORDER BY position, uuid`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return nil, err
	}
	out := make([]*domain.Lane, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out, nil
}

// CountLanes reports how many lanes a project has, so a create can refuse to go
// past domain.MaxLanesPerProject.
func (s *Store) CountLanes(ctx context.Context, projectUUID string) (int, error) {
	var n int
	if _, err := database.GetNamed(ctx, s.tx.Reader(ctx), &n,
		`SELECT count(*) FROM lanes WHERE project_uuid = :project_uuid`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return 0, err
	}
	return n, nil
}

// UpdateLane writes back everything a manager may edit except the position.
func (s *Store) UpdateLane(ctx context.Context, l *domain.Lane) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE lanes SET
			name       = :name,
			color      = :color,
			wip_limit  = :wip_limit,
			is_done    = :is_done,
			updated_at = :updated_at
		WHERE uuid = :uuid`,
		toLaneModel(l))
	if err != nil {
		return database.MapError(err, "update lane")
	}
	return database.RequireRow(n, "that lane no longer exists")
}

// ReorderLanes rewrites positions for a whole board in one transaction.
//
// Every lane is renumbered rather than only the ones that moved. That is why
// (project_uuid, position) is indexed but deliberately not unique: a partial
// state part-way through this loop would violate a unique constraint even though
// the end state is fine.
//
// Each statement is scoped to the project, so an order naming a lane from another
// board updates nothing and the row count catches it.
func (s *Store) ReorderLanes(ctx context.Context, projectUUID string, order []string, at database.Time) error {
	positions, err := domain.Reorder(order)
	if err != nil {
		return err
	}

	return s.tx.InTx(ctx, func(ctx context.Context) error {
		for uuid, position := range positions {
			n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
				UPDATE lanes SET position = :position, updated_at = :at
				WHERE uuid = :uuid AND project_uuid = :project_uuid`,
				map[string]any{
					"position": position, "at": at,
					"uuid": uuid, "project_uuid": projectUUID,
				})
			if err != nil {
				return database.MapError(err, "reorder lanes")
			}
			if n == 0 {
				return apperrors.NotFound("that lane is not on this board")
			}
		}
		return nil
	})
}

// DeleteLane removes a lane, first moving any cards it holds into moveTo.
//
// cards.lane_uuid is ON DELETE RESTRICT, so a lane holding cards cannot simply be
// dropped: the database refuses, which is the point. An empty moveTo is therefore
// only valid for an empty lane.
func (s *Store) DeleteLane(ctx context.Context, projectUUID, laneUUID, moveTo string) error {
	if laneUUID == moveTo {
		return apperrors.Invalid("a lane cannot be emptied into itself")
	}

	return s.tx.InTx(ctx, func(ctx context.Context) error {
		var held int
		if _, err := database.GetNamed(ctx, s.tx.Reader(ctx), &held,
			`SELECT count(*) FROM cards WHERE lane_uuid = :lane_uuid`,
			map[string]any{"lane_uuid": laneUUID}); err != nil {
			return err
		}

		if held > 0 {
			if moveTo == "" {
				return apperrors.Conflict(
					"that lane still holds cards; choose where they should go")
			}
			var target int
			if _, err := database.GetNamed(ctx, s.tx.Reader(ctx), &target, `
				SELECT count(*) FROM lanes
				WHERE uuid = :uuid AND project_uuid = :project_uuid`,
				map[string]any{"uuid": moveTo, "project_uuid": projectUUID}); err != nil {
				return err
			}
			if target == 0 {
				return apperrors.NotFound("that lane is not on this board")
			}

			// The offset is read as a value first. A named parameter binds a
			// value, not SQL, so it cannot carry a subquery -- and reading it
			// inside this transaction is what makes it consistent with the
			// update that follows.
			var offset int
			if _, err := database.GetNamed(ctx, s.tx.Reader(ctx), &offset, `
				SELECT coalesce(max(position) + 1, 0) FROM cards
				WHERE lane_uuid = :lane_uuid`,
				map[string]any{"lane_uuid": moveTo}); err != nil {
				return err
			}

			// Appended after whatever the target already holds, preserving the
			// relative order of the cards being moved.
			if _, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
				UPDATE cards SET
					lane_uuid  = :move_to,
					position   = position + :offset,
					updated_at = :at
				WHERE lane_uuid = :lane_uuid`,
				map[string]any{
					"move_to": moveTo, "offset": offset,
					"lane_uuid": laneUUID, "at": database.Now(),
				}); err != nil {
				return database.MapError(err, "empty lane")
			}
		}

		n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
			DELETE FROM lanes
			WHERE uuid = :uuid AND project_uuid = :project_uuid`,
			map[string]any{"uuid": laneUUID, "project_uuid": projectUUID})
		if err != nil {
			return database.MapError(err, "delete lane")
		}
		return database.RequireRow(n, "that lane no longer exists")
	})
}

const labelColumns = `uuid, project_uuid, name, color, created_at`

type labelModel struct {
	UUID        string        `db:"uuid"`
	ProjectUUID string        `db:"project_uuid"`
	Name        string        `db:"name"`
	Colour      string        `db:"color"`
	CreatedAt   database.Time `db:"created_at"`
}

func (m labelModel) toDomain() *domain.Label {
	return &domain.Label{
		UUID:        m.UUID,
		ProjectUUID: m.ProjectUUID,
		Name:        m.Name,
		Colour:      domain.Colour(m.Colour),
		CreatedAt:   m.CreatedAt.Time(),
	}
}

// CreateLabel inserts a label.
func (s *Store) CreateLabel(ctx context.Context, l *domain.Label) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO labels (`+labelColumns+`)
		VALUES (:uuid, :project_uuid, :name, :color, :created_at)`,
		labelModel{
			UUID: l.UUID, ProjectUUID: l.ProjectUUID, Name: l.Name,
			Colour: string(l.Colour), CreatedAt: database.NewTime(l.CreatedAt),
		})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return apperrors.Conflict("this project already has a label with that name").
				WithField("name", "is already used").
				Wrap(err)
		}
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid("that project no longer exists").Wrap(err)
		}
		return database.MapError(err, "create label")
	}
	return nil
}

// LabelsForProject returns a project's labels in display order.
func (s *Store) LabelsForProject(ctx context.Context, projectUUID string) ([]*domain.Label, error) {
	var models []labelModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT `+labelColumns+` FROM labels
		WHERE project_uuid = :project_uuid
		ORDER BY name, uuid`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return nil, err
	}
	out := make([]*domain.Label, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out, nil
}

// LabelByUUID returns the label, or (nil, nil) when there is none.
func (s *Store) LabelByUUID(ctx context.Context, uuid string) (*domain.Label, error) {
	var m labelModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m,
		`SELECT `+labelColumns+` FROM labels WHERE uuid = :uuid`,
		map[string]any{"uuid": uuid})
	if err != nil || !found {
		return nil, err
	}
	return m.toDomain(), nil
}

// UpdateLabel renames a label.
func (s *Store) UpdateLabel(ctx context.Context, l *domain.Label) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE labels SET name = :name, color = :color WHERE uuid = :uuid`,
		map[string]any{"name": l.Name, "color": string(l.Colour), "uuid": l.UUID})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return apperrors.Conflict("this project already has a label with that name").
				WithField("name", "is already used").
				Wrap(err)
		}
		return database.MapError(err, "update label")
	}
	return database.RequireRow(n, "that label no longer exists")
}

// DeleteLabel removes a label. Its assignments to cards cascade.
func (s *Store) DeleteLabel(ctx context.Context, projectUUID, uuid string) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		DELETE FROM labels WHERE uuid = :uuid AND project_uuid = :project_uuid`,
		map[string]any{"uuid": uuid, "project_uuid": projectUUID})
	if err != nil {
		return database.MapError(err, "delete label")
	}
	return database.RequireRow(n, "that label no longer exists")
}
