package board_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/board"
	"github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

type fixture struct {
	store   *board.Store
	db      *database.DB
	ctx     context.Context
	owner   string
	project string
}

func setup(t *testing.T) fixture {
	t.Helper()
	db, tx := dbtest.NewWithTx(t)
	ctx := context.Background()
	now := database.Now()

	owner := idgenerator.NewUUIDv7()
	_, err := db.Writer().NamedExecContext(ctx, `
		INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
		VALUES (:uuid, 'owner@example.com', 'Owner', 'hash', 'super_admin', :now, :now)`,
		map[string]any{"uuid": owner, "now": now})
	require.NoError(t, err)

	proj := idgenerator.NewUUIDv7()
	_, err = db.Writer().NamedExecContext(ctx, `
		INSERT INTO projects (uuid, slug, name, created_by, created_at, updated_at)
		VALUES (:uuid, 'chonk', 'Chonkboard', :owner, :now, :now)`,
		map[string]any{"uuid": proj, "owner": owner, "now": now})
	require.NoError(t, err)

	return fixture{store: board.NewStore(tx), db: db, ctx: ctx, owner: owner, project: proj}
}

func (f fixture) addLane(t *testing.T, name string, wip *int) *domain.Lane {
	t.Helper()
	position, err := f.store.NextLanePosition(f.ctx, f.project)
	require.NoError(t, err)

	lane, err := domain.NewLane(idgenerator.NewUUIDv7(), f.project, name, position,
		domain.ColourSlate, wip, false, time.Now())
	require.NoError(t, err)
	require.NoError(t, f.store.CreateLane(f.ctx, lane))
	return lane
}

func (f fixture) addCard(t *testing.T, laneUUID, title string, position int) string {
	t.Helper()
	uuid := idgenerator.NewUUIDv7()
	now := database.Now()
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO cards (uuid, project_uuid, lane_uuid, title, position,
		                   created_by, created_at, updated_at)
		VALUES (:uuid, :project, :lane, :title, :position, :owner, :now, :now)`,
		map[string]any{
			"uuid": uuid, "project": f.project, "lane": laneUUID, "title": title,
			"position": position, "owner": f.owner, "now": now,
		})
	require.NoError(t, err)
	return uuid
}

func (f fixture) laneOf(t *testing.T, cardUUID string) (lane string, position int) {
	t.Helper()
	var row struct {
		LaneUUID string `db:"lane_uuid"`
		Position int    `db:"position"`
	}
	require.NoError(t, f.db.Reader().GetContext(f.ctx, &row,
		`SELECT lane_uuid, position FROM cards WHERE uuid = ?`, cardUUID))
	return row.LaneUUID, row.Position
}

func TestLanesGetDensePositionsInCreationOrder(t *testing.T) {
	f := setup(t)
	names := []string{"backlog", "in-progress", "testing", "done", "stash"}
	for _, name := range names {
		f.addLane(t, name, nil)
	}

	lanes, err := f.store.LanesForProject(f.ctx, f.project)
	require.NoError(t, err)
	require.Len(t, lanes, len(names))
	for i, lane := range lanes {
		assert.Equal(t, names[i], lane.Name)
		assert.Equal(t, i, lane.Position, "positions must be dense and zero-based")
	}

	n, err := f.store.CountLanes(f.ctx, f.project)
	require.NoError(t, err)
	assert.Equal(t, len(names), n)
}

func TestUpdateLane(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, "in-progress", nil)

	limit := 3
	require.NoError(t, lane.Update("In progress", domain.ColourAmber, &limit, false, time.Now()))
	require.NoError(t, f.store.UpdateLane(f.ctx, lane))

	got, err := f.store.LaneByUUID(f.ctx, lane.UUID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "In progress", got.Name)
	assert.Equal(t, domain.ColourAmber, got.Colour)
	require.NotNil(t, got.WIPLimit)
	assert.Equal(t, 3, *got.WIPLimit)
	assert.Equal(t, 3, got.Limit())

	require.NoError(t, got.Update("In progress", domain.ColourAmber, nil, true, time.Now()))
	require.NoError(t, f.store.UpdateLane(f.ctx, got))

	cleared, err := f.store.LaneByUUID(f.ctx, lane.UUID)
	require.NoError(t, err)
	assert.Nil(t, cleared.WIPLimit, "an empty limit means no limit")
	assert.Zero(t, cleared.Limit())
	assert.True(t, cleared.IsDone)
}

func TestLaneWithAZeroWIPLimitIsRefusedByTheSchema(t *testing.T) {
	// A lane nothing can enter is a mistake rather than a policy, and the CHECK
	// says so even if a caller bypasses the domain constructor.
	f := setup(t)
	zero := 0
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO lanes (uuid, project_uuid, name, position, wip_limit, created_at, updated_at)
		VALUES (:uuid, :project, 'broken', 0, :wip, :now, :now)`,
		map[string]any{
			"uuid": idgenerator.NewUUIDv7(), "project": f.project,
			"wip": zero, "now": database.Now(),
		})
	require.Error(t, err)
	assert.True(t, database.IsCheckViolation(err))
	assert.Equal(t, "lanes_wip_limit_check", database.Constraint(err))
}

func TestReorderLanesRenumbersTheWholeBoard(t *testing.T) {
	f := setup(t)
	a := f.addLane(t, "a", nil)
	b := f.addLane(t, "b", nil)
	c := f.addLane(t, "c", nil)

	require.NoError(t, f.store.ReorderLanes(f.ctx, f.project,
		[]string{c.UUID, a.UUID, b.UUID}, database.Now()))

	lanes, err := f.store.LanesForProject(f.ctx, f.project)
	require.NoError(t, err)
	require.Len(t, lanes, 3)
	assert.Equal(t, []string{"c", "a", "b"},
		[]string{lanes[0].Name, lanes[1].Name, lanes[2].Name})
	for i, lane := range lanes {
		assert.Equal(t, i, lane.Position)
	}
}

func TestReorderRefusesAForeignOrRepeatedLane(t *testing.T) {
	f := setup(t)
	a := f.addLane(t, "a", nil)
	b := f.addLane(t, "b", nil)

	t.Run("a lane from another board", func(t *testing.T) {
		err := f.store.ReorderLanes(f.ctx, f.project,
			[]string{a.UUID, b.UUID, idgenerator.NewUUIDv7()}, database.Now())
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))
	})

	t.Run("the same lane twice", func(t *testing.T) {
		err := f.store.ReorderLanes(f.ctx, f.project,
			[]string{a.UUID, a.UUID}, database.Now())
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
	})

	t.Run("an empty order", func(t *testing.T) {
		err := f.store.ReorderLanes(f.ctx, f.project, nil, database.Now())
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
	})

	// A rejected reorder must leave the board exactly as it was.
	lanes, err := f.store.LanesForProject(f.ctx, f.project)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, []string{lanes[0].Name, lanes[1].Name})
	assert.Equal(t, 0, lanes[0].Position)
	assert.Equal(t, 1, lanes[1].Position)
}

func TestDeleteEmptyLane(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, "spare", nil)

	require.NoError(t, f.store.DeleteLane(f.ctx, f.project, lane.UUID, ""))

	got, err := f.store.LaneByUUID(f.ctx, lane.UUID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDeleteLaneHoldingCardsNeedsSomewhereToPutThem(t *testing.T) {
	f := setup(t)
	from := f.addLane(t, "going", nil)
	f.addCard(t, from.UUID, "keep me", 0)

	err := f.store.DeleteLane(f.ctx, f.project, from.UUID, "")

	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeConflict, ""))

	still, err := f.store.LaneByUUID(f.ctx, from.UUID)
	require.NoError(t, err)
	assert.NotNil(t, still, "the lane must survive a refused delete")
}

func TestDeleteLaneMovesItsCardsAndAppendsThem(t *testing.T) {
	f := setup(t)
	from := f.addLane(t, "going", nil)
	to := f.addLane(t, "staying", nil)

	existing := f.addCard(t, to.UUID, "already here", 0)
	first := f.addCard(t, from.UUID, "first", 0)
	second := f.addCard(t, from.UUID, "second", 1)

	require.NoError(t, f.store.DeleteLane(f.ctx, f.project, from.UUID, to.UUID))

	gone, err := f.store.LaneByUUID(f.ctx, from.UUID)
	require.NoError(t, err)
	assert.Nil(t, gone)

	// Appended after what the target already held, keeping their relative order.
	for card, want := range map[string]int{existing: 0, first: 1, second: 2} {
		lane, position := f.laneOf(t, card)
		assert.Equal(t, to.UUID, lane)
		assert.Equal(t, want, position)
	}
}

func TestDeleteLaneRefusesAForeignTargetAndItself(t *testing.T) {
	f := setup(t)
	from := f.addLane(t, "going", nil)
	f.addCard(t, from.UUID, "card", 0)

	err := f.store.DeleteLane(f.ctx, f.project, from.UUID, from.UUID)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""),
		"a lane cannot be emptied into itself")

	err = f.store.DeleteLane(f.ctx, f.project, from.UUID, idgenerator.NewUUIDv7())
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))

	still, err := f.store.LaneByUUID(f.ctx, from.UUID)
	require.NoError(t, err)
	assert.NotNil(t, still)
}

func TestLaneForAnAbsentProjectIsRefused(t *testing.T) {
	f := setup(t)
	lane, err := domain.NewLane(idgenerator.NewUUIDv7(), idgenerator.NewUUIDv7(),
		"orphan", 0, domain.ColourSlate, nil, false, time.Now())
	require.NoError(t, err)

	err = f.store.CreateLane(f.ctx, lane)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
}

func TestLabelsAreScopedToTheirProject(t *testing.T) {
	f := setup(t)

	label, err := domain.NewLabel(idgenerator.NewUUIDv7(), f.project, "bug", domain.ColourRose, time.Now())
	require.NoError(t, err)
	require.NoError(t, f.store.CreateLabel(f.ctx, label))

	labels, err := f.store.LabelsForProject(f.ctx, f.project)
	require.NoError(t, err)
	require.Len(t, labels, 1)
	assert.Equal(t, "bug", labels[0].Name)
	assert.Equal(t, domain.ColourRose, labels[0].Colour)

	t.Run("a duplicate name in the same project is a field error", func(t *testing.T) {
		dup, err := domain.NewLabel(idgenerator.NewUUIDv7(), f.project, "bug", domain.ColourBlue, time.Now())
		require.NoError(t, err)

		err = f.store.CreateLabel(f.ctx, dup)
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeConflict, ""))
		require.Len(t, apperrors.From(err).Fields, 1)
		assert.Equal(t, "name", apperrors.From(err).Fields[0].Field)
	})

	t.Run("rename and delete", func(t *testing.T) {
		require.NoError(t, label.Rename("defect", domain.ColourAmber, time.Now()))
		require.NoError(t, f.store.UpdateLabel(f.ctx, label))

		got, err := f.store.LabelByUUID(f.ctx, label.UUID)
		require.NoError(t, err)
		assert.Equal(t, "defect", got.Name)
		assert.Equal(t, domain.ColourAmber, got.Colour)

		require.NoError(t, f.store.DeleteLabel(f.ctx, f.project, label.UUID))
		gone, err := f.store.LabelByUUID(f.ctx, label.UUID)
		require.NoError(t, err)
		assert.Nil(t, gone)

		assert.ErrorIs(t, f.store.DeleteLabel(f.ctx, f.project, label.UUID),
			apperrors.New(apperrors.CodeNotFound, ""))
	})
}

func TestColourOutsideThePaletteIsRefusedByTheSchema(t *testing.T) {
	// The palette is closed because a lane colour has to match a Tailwind class
	// that exists at build time. A hex code here would render unstyled.
	f := setup(t)
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO lanes (uuid, project_uuid, name, position, color, created_at, updated_at)
		VALUES (:uuid, :project, 'broken', 0, '#ff0000', :now, :now)`,
		map[string]any{
			"uuid": idgenerator.NewUUIDv7(), "project": f.project, "now": database.Now(),
		})
	require.Error(t, err)
	assert.Equal(t, "lanes_color_check", database.Constraint(err))
}
