package card_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/card"
	"github.com/AzmainMahtab/chonkboard/internal/card/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

type fixture struct {
	store   *card.Store
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

	return fixture{store: card.NewStore(tx), db: db, ctx: ctx, owner: owner, project: proj}
}

// newProject makes a second board in the same database, so a test can prove that
// a card or lane from elsewhere is refused.
func (f fixture) newProject(t *testing.T, slug string) string {
	t.Helper()
	uuid := idgenerator.NewUUIDv7()
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO projects (uuid, slug, name, created_by, created_at, updated_at)
		VALUES (:uuid, :slug, :slug, :owner, :now, :now)`,
		map[string]any{"uuid": uuid, "slug": slug, "owner": f.owner, "now": database.Now()})
	require.NoError(t, err)
	return uuid
}

func (f fixture) addLane(t *testing.T, projectUUID, name string, position int, wip *int) string {
	t.Helper()
	uuid := idgenerator.NewUUIDv7()
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO lanes (uuid, project_uuid, name, position, wip_limit, created_at, updated_at)
		VALUES (:uuid, :project, :name, :position, :wip, :now, :now)`,
		map[string]any{
			"uuid": uuid, "project": projectUUID, "name": name,
			"position": position, "wip": wip, "now": database.Now(),
		})
	require.NoError(t, err)
	return uuid
}

func (f fixture) addCard(t *testing.T, projectUUID, laneUUID, title string) *domain.Card {
	t.Helper()
	position, err := f.store.NextPosition(f.ctx, laneUUID)
	require.NoError(t, err)

	c, err := domain.NewCard(idgenerator.NewUUIDv7(), projectUUID, laneUUID, title, f.owner,
		position, time.Now())
	require.NoError(t, err)
	require.NoError(t, f.store.Create(f.ctx, c))
	return c
}

// order returns the card titles in a lane, in stored order.
func (f fixture) order(t *testing.T, laneUUID string) []string {
	t.Helper()
	cards, err := f.store.ForLane(f.ctx, laneUUID)
	require.NoError(t, err)

	titles := make([]string, 0, len(cards))
	for i, c := range cards {
		assert.Equal(t, i, c.Position, "positions must stay dense and zero-based")
		titles = append(titles, c.Title)
	}
	return titles
}

func TestCreateCardAppendsToTheLane(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)

	for _, title := range []string{"first", "second", "third"} {
		f.addCard(t, f.project, lane, title)
	}

	assert.Equal(t, []string{"first", "second", "third"}, f.order(t, lane))
}

func TestCardRoundTripsEveryField(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	c := f.addCard(t, f.project, lane, "needs detail")

	due := time.Date(2026, 11, 3, 17, 0, 0, 0, time.UTC)
	assignee := f.owner
	require.NoError(t, c.Edit("Ship the thing", "A **markdown** body.",
		domain.PriorityHigh, &assignee, &due, time.Now()))
	require.NoError(t, f.store.Update(f.ctx, c))

	got, err := f.store.ByUUID(f.ctx, c.UUID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Ship the thing", got.Title)
	assert.Equal(t, "A **markdown** body.", got.Description)
	assert.Equal(t, domain.PriorityHigh, got.Priority)
	require.NotNil(t, got.AssigneeUUID)
	assert.Equal(t, f.owner, *got.AssigneeUUID)
	require.NotNil(t, got.DueAt)
	assert.True(t, due.Equal(*got.DueAt))
	assert.False(t, got.IsArchived())
	assert.True(t, got.IsOverdue(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), false))
	assert.False(t, got.IsOverdue(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), true),
		"a card in a done lane is never overdue")
}

func TestClearingOptionalFields(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	c := f.addCard(t, f.project, lane, "card")

	due := time.Now().Add(time.Hour)
	assignee := f.owner
	require.NoError(t, c.Edit("card", "", domain.PriorityLow, &assignee, &due, time.Now()))
	require.NoError(t, f.store.Update(f.ctx, c))

	require.NoError(t, c.Edit("card", "", domain.PriorityNone, nil, nil, time.Now()))
	require.NoError(t, f.store.Update(f.ctx, c))

	got, err := f.store.ByUUID(f.ctx, c.UUID)
	require.NoError(t, err)
	assert.Nil(t, got.AssigneeUUID, "a cleared assignee must become NULL")
	assert.Nil(t, got.DueAt, "a cleared due date must become NULL")
}

func TestArchivedCardsLeaveTheBoard(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	kept := f.addCard(t, f.project, lane, "kept")
	stashed := f.addCard(t, f.project, lane, "stashed")

	stashed.Archive(time.Now())
	require.NoError(t, f.store.Update(f.ctx, stashed))

	assert.Equal(t, []string{"kept"}, f.order(t, lane))

	live, err := f.store.ForProject(f.ctx, f.project)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, kept.UUID, live[0].UUID)

	// Still reachable directly, so its history can be shown.
	got, err := f.store.ByUUID(f.ctx, stashed.UUID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.IsArchived())
}

func TestMoveWithinALaneRenumbersDensely(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	a := f.addCard(t, f.project, lane, "a")
	b := f.addCard(t, f.project, lane, "b")
	c := f.addCard(t, f.project, lane, "c")

	affected, err := f.store.Move(f.ctx, f.project, domain.Move{
		CardUUID: c.UUID,
		ToLane:   lane,
		ToOrder:  []string{c.UUID, a.UUID, b.UUID},
	}, time.Now())

	require.NoError(t, err)
	assert.Equal(t, []string{lane}, affected)
	assert.Equal(t, []string{"c", "a", "b"}, f.order(t, lane))
}

func TestMoveAcrossLanesRewritesBoth(t *testing.T) {
	f := setup(t)
	from := f.addLane(t, f.project, "backlog", 0, nil)
	to := f.addLane(t, f.project, "doing", 1, nil)

	a := f.addCard(t, f.project, from, "a")
	b := f.addCard(t, f.project, from, "b")
	existing := f.addCard(t, f.project, to, "existing")

	affected, err := f.store.Move(f.ctx, f.project, domain.Move{
		CardUUID:  a.UUID,
		ToLane:    to,
		ToOrder:   []string{existing.UUID, a.UUID},
		FromLane:  from,
		FromOrder: []string{b.UUID},
	}, time.Now())

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{from, to}, affected,
		"both lanes changed, so both must be broadcast")
	assert.Equal(t, []string{"b"}, f.order(t, from))
	assert.Equal(t, []string{"existing", "a"}, f.order(t, to))
}

func TestMoveIntoAnEmptyLane(t *testing.T) {
	f := setup(t)
	from := f.addLane(t, f.project, "backlog", 0, nil)
	to := f.addLane(t, f.project, "doing", 1, nil)
	only := f.addCard(t, f.project, from, "only")

	_, err := f.store.Move(f.ctx, f.project, domain.Move{
		CardUUID:  only.UUID,
		ToLane:    to,
		ToOrder:   []string{only.UUID},
		FromLane:  from,
		FromOrder: []string{},
	}, time.Now())

	require.NoError(t, err)
	assert.Empty(t, f.order(t, from))
	assert.Equal(t, []string{"only"}, f.order(t, to))
}

func TestMoveRefusedByAWIPLimitChangesNothing(t *testing.T) {
	// The transaction is what makes this a clean refusal rather than a
	// half-applied move.
	f := setup(t)
	limit := 2
	from := f.addLane(t, f.project, "backlog", 0, nil)
	to := f.addLane(t, f.project, "doing", 1, &limit)

	moving := f.addCard(t, f.project, from, "moving")
	staying := f.addCard(t, f.project, from, "staying")
	full1 := f.addCard(t, f.project, to, "full-1")
	full2 := f.addCard(t, f.project, to, "full-2")

	_, err := f.store.Move(f.ctx, f.project, domain.Move{
		CardUUID:  moving.UUID,
		ToLane:    to,
		ToOrder:   []string{full1.UUID, full2.UUID, moving.UUID},
		FromLane:  from,
		FromOrder: []string{staying.UUID},
	}, time.Now())

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrWIPExceeded)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeConflict, ""))

	assert.Equal(t, []string{"moving", "staying"}, f.order(t, from), "source untouched")
	assert.Equal(t, []string{"full-1", "full-2"}, f.order(t, to), "target untouched")
}

func TestMoveRefusesACardFromAnotherBoard(t *testing.T) {
	// Scoping every UPDATE to the project is the authorisation check: a member of
	// one board must not be able to reorder another's cards by naming their ids.
	f := setup(t)
	mine := f.addLane(t, f.project, "backlog", 0, nil)
	ours := f.addCard(t, f.project, mine, "ours")

	other := f.newProject(t, "other")
	theirLane := f.addLane(t, other, "theirs", 0, nil)
	theirs := f.addCard(t, other, theirLane, "theirs")

	t.Run("moving their card onto my board", func(t *testing.T) {
		_, err := f.store.Move(f.ctx, f.project, domain.Move{
			CardUUID: theirs.UUID,
			ToLane:   mine,
			ToOrder:  []string{ours.UUID, theirs.UUID},
		}, time.Now())
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))
	})

	t.Run("naming their card in my order", func(t *testing.T) {
		_, err := f.store.Move(f.ctx, f.project, domain.Move{
			CardUUID: ours.UUID,
			ToLane:   mine,
			ToOrder:  []string{theirs.UUID, ours.UUID},
		}, time.Now())
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))
	})

	t.Run("moving my card into their lane", func(t *testing.T) {
		_, err := f.store.Move(f.ctx, f.project, domain.Move{
			CardUUID: ours.UUID,
			ToLane:   theirLane,
			ToOrder:  []string{ours.UUID},
		}, time.Now())
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))
	})

	assert.Equal(t, []string{"ours"}, f.order(t, mine))
	assert.Equal(t, []string{"theirs"}, f.order(t, theirLane))
}

func TestMoveRefusesAnOrderThatOmitsTheCard(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	a := f.addCard(t, f.project, lane, "a")
	b := f.addCard(t, f.project, lane, "b")

	_, err := f.store.Move(f.ctx, f.project, domain.Move{
		CardUUID: a.UUID,
		ToLane:   lane,
		ToOrder:  []string{b.UUID},
	}, time.Now())

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrCardMissing)
	assert.Equal(t, []string{"a", "b"}, f.order(t, lane))
}

func TestSnapshotIsReadInsideTheMovesTransaction(t *testing.T) {
	f := setup(t)
	limit := 1
	lane := f.addLane(t, f.project, "doing", 0, &limit)
	f.addCard(t, f.project, lane, "resident")

	board, err := f.store.Snapshot(f.ctx, f.project)
	require.NoError(t, err)

	require.Contains(t, board.Lanes, lane)
	assert.Equal(t, 1, board.Lanes[lane].WIPLimit)
	assert.Len(t, board.LaneOfCard, 1, "archived cards are excluded from the snapshot")
}

func TestSetLabels(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	c := f.addCard(t, f.project, lane, "labelled")

	bug := f.addLabel(t, f.project, "bug")
	chore := f.addLabel(t, f.project, "chore")

	require.NoError(t, f.store.SetLabels(f.ctx, c.UUID, []string{bug, chore}))

	byCard, err := f.store.LabelUUIDsForCards(f.ctx, []string{c.UUID})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{bug, chore}, byCard[c.UUID])

	t.Run("replacing the set removes what is not in it", func(t *testing.T) {
		require.NoError(t, f.store.SetLabels(f.ctx, c.UUID, []string{chore}))

		byCard, err := f.store.LabelUUIDsForCards(f.ctx, []string{c.UUID})
		require.NoError(t, err)
		assert.Equal(t, []string{chore}, byCard[c.UUID])
	})

	t.Run("clearing removes them all", func(t *testing.T) {
		require.NoError(t, f.store.SetLabels(f.ctx, c.UUID, nil))

		byCard, err := f.store.LabelUUIDsForCards(f.ctx, []string{c.UUID})
		require.NoError(t, err)
		assert.Empty(t, byCard[c.UUID])
	})
}

func TestSetLabelsRefusesALabelFromAnotherBoard(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	c := f.addCard(t, f.project, lane, "card")

	other := f.newProject(t, "other")
	foreign := f.addLabel(t, other, "theirs")
	mine := f.addLabel(t, f.project, "mine")

	err := f.store.SetLabels(f.ctx, c.UUID, []string{mine, foreign})

	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))

	byCard, err := f.store.LabelUUIDsForCards(f.ctx, []string{c.UUID})
	require.NoError(t, err)
	assert.Empty(t, byCard[c.UUID], "the whole set is rolled back")
}

func (f fixture) addLabel(t *testing.T, projectUUID, name string) string {
	t.Helper()
	uuid := idgenerator.NewUUIDv7()
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO labels (uuid, project_uuid, name, created_at)
		VALUES (:uuid, :project, :name, :now)`,
		map[string]any{
			"uuid": uuid, "project": projectUUID, "name": name, "now": database.Now(),
		})
	require.NoError(t, err)
	return uuid
}

func TestLabelUUIDsForCardsHandlesAnEmptyRequest(t *testing.T) {
	// An empty IN list would be invalid SQL, so the query must not run at all.
	f := setup(t)
	byCard, err := f.store.LabelUUIDsForCards(f.ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, byCard)
}

func TestActivityIsRecordedAndReadBackNewestFirst(t *testing.T) {
	f := setup(t)
	from := f.addLane(t, f.project, "backlog", 0, nil)
	to := f.addLane(t, f.project, "doing", 1, nil)
	c := f.addCard(t, f.project, from, "card")

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, f.store.RecordActivity(f.ctx, card.Activity{
		UUID: idgenerator.NewUUIDv7(), CardUUID: c.UUID, ProjectUUID: f.project,
		ActorUUID: f.owner, Kind: card.ActivityCreated, CreatedAt: base,
	}))
	require.NoError(t, f.store.RecordActivity(f.ctx, card.Activity{
		UUID: idgenerator.NewUUIDv7(), CardUUID: c.UUID, ProjectUUID: f.project,
		ActorUUID: f.owner, Kind: card.ActivityMoved,
		FromLaneUUID: &from, ToLaneUUID: &to,
		Meta: `{"reason":"drag"}`, CreatedAt: base.Add(time.Minute),
	}))

	history, err := f.store.ActivityForCard(f.ctx, c.UUID, 10)
	require.NoError(t, err)
	require.Len(t, history, 2)

	assert.Equal(t, card.ActivityMoved, history[0].Kind, "newest first")
	require.NotNil(t, history[0].FromLaneUUID)
	assert.Equal(t, from, *history[0].FromLaneUUID)
	assert.Equal(t, `{"reason":"drag"}`, history[0].Meta)

	assert.Equal(t, card.ActivityCreated, history[1].Kind)
	assert.Equal(t, "{}", history[1].Meta, "an empty meta defaults to an empty document")
}

func TestUnknownActivityKindIsRefusedByTheSchema(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	c := f.addCard(t, f.project, lane, "card")

	err := f.store.RecordActivity(f.ctx, card.Activity{
		UUID: idgenerator.NewUUIDv7(), CardUUID: c.UUID, ProjectUUID: f.project,
		ActorUUID: f.owner, Kind: "teleported", CreatedAt: time.Now(),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
}

func TestDeletingACardTakesItsChildren(t *testing.T) {
	f := setup(t)
	lane := f.addLane(t, f.project, "backlog", 0, nil)
	c := f.addCard(t, f.project, lane, "going")
	label := f.addLabel(t, f.project, "bug")
	require.NoError(t, f.store.SetLabels(f.ctx, c.UUID, []string{label}))
	require.NoError(t, f.store.RecordActivity(f.ctx, card.Activity{
		UUID: idgenerator.NewUUIDv7(), CardUUID: c.UUID, ProjectUUID: f.project,
		ActorUUID: f.owner, Kind: card.ActivityCreated, CreatedAt: time.Now(),
	}))

	require.NoError(t, f.store.Delete(f.ctx, f.project, c.UUID))

	gone, err := f.store.ByUUID(f.ctx, c.UUID)
	require.NoError(t, err)
	assert.Nil(t, gone)

	history, err := f.store.ActivityForCard(f.ctx, c.UUID, 10)
	require.NoError(t, err)
	assert.Empty(t, history, "activity cascades with the card")

	assert.ErrorIs(t, f.store.Delete(f.ctx, f.project, c.UUID),
		apperrors.New(apperrors.CodeNotFound, ""))
}

func TestDeleteRefusesACardFromAnotherBoard(t *testing.T) {
	f := setup(t)
	other := f.newProject(t, "other")
	theirLane := f.addLane(t, other, "theirs", 0, nil)
	theirs := f.addCard(t, other, theirLane, "theirs")

	err := f.store.Delete(f.ctx, f.project, theirs.UUID)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))

	still, err := f.store.ByUUID(f.ctx, theirs.UUID)
	require.NoError(t, err)
	assert.NotNil(t, still)
}

func TestCardForAnAbsentLaneIsRefused(t *testing.T) {
	f := setup(t)
	c, err := domain.NewCard(idgenerator.NewUUIDv7(), f.project, idgenerator.NewUUIDv7(),
		"orphan", f.owner, 0, time.Now())
	require.NoError(t, err)

	err = f.store.Create(f.ctx, c)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
}
