package board_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/board"
	"github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	projectdomain "github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

type serviceFixture struct {
	svc     *board.Service
	db      *database.DB
	ctx     context.Context
	project string
	owner   string
}

func newService(t *testing.T) *serviceFixture {
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

	return &serviceFixture{
		svc:     board.NewService(board.NewStore(tx), tx, dbtest.Discard()),
		db:      db,
		ctx:     ctx,
		project: proj,
		owner:   owner,
	}
}

// The four standings, as authz sees them.
func operator() authz.Subject {
	return authz.NewSubject(
		&authdomain.User{UUID: "op", Role: authdomain.RoleSuperAdmin, Status: authdomain.StatusActive}, nil)
}

func manager() authz.Subject {
	role := projectdomain.RoleManager
	return authz.NewSubject(
		&authdomain.User{UUID: "mgr", Role: authdomain.RoleMember, Status: authdomain.StatusActive}, &role)
}

func member() authz.Subject {
	role := projectdomain.RoleMember
	return authz.NewSubject(
		&authdomain.User{UUID: "mem", Role: authdomain.RoleMember, Status: authdomain.StatusActive}, &role)
}

func stranger() authz.Subject {
	return authz.NewSubject(
		&authdomain.User{UUID: "str", Role: authdomain.RoleMember, Status: authdomain.StatusActive}, nil)
}

func laneInput(name string) board.LaneInput {
	return board.LaneInput{Name: name, Colour: domain.ColourSlate}
}

// TestOnlyAManagerCanChangeTheBoardsShape is the requirement, at the service
// boundary.
//
// The handler checks too, but this is the check that cannot be forgotten: there is
// no way to call these methods without presenting a subject, so a route added later
// without its own guard still cannot let a member through.
func TestOnlyAManagerCanChangeTheBoardsShape(t *testing.T) {
	lane := func(t *testing.T, f *serviceFixture) string {
		t.Helper()
		l, err := f.svc.CreateLane(f.ctx, operator(), f.project, laneInput("seed"))
		require.NoError(t, err)
		return l.UUID
	}

	operations := map[string]func(f *serviceFixture, subj authz.Subject, laneUUID string) error{
		"create a lane": func(f *serviceFixture, subj authz.Subject, _ string) error {
			_, err := f.svc.CreateLane(f.ctx, subj, f.project, laneInput("new"))
			return err
		},
		"rename a lane": func(f *serviceFixture, subj authz.Subject, laneUUID string) error {
			_, err := f.svc.UpdateLane(f.ctx, subj, f.project, laneUUID, laneInput("renamed"))
			return err
		},
		"reorder lanes": func(f *serviceFixture, subj authz.Subject, laneUUID string) error {
			return f.svc.ReorderLanes(f.ctx, subj, f.project, []string{laneUUID})
		},
		"delete a lane": func(f *serviceFixture, subj authz.Subject, laneUUID string) error {
			return f.svc.DeleteLane(f.ctx, subj, f.project, laneUUID, "")
		},
		"create a label": func(f *serviceFixture, subj authz.Subject, _ string) error {
			_, err := f.svc.CreateLabel(f.ctx, subj, f.project, board.LabelInput{
				Name: "bug", Colour: domain.ColourRose,
			})
			return err
		},
		"delete a label": func(f *serviceFixture, subj authz.Subject, _ string) error {
			return f.svc.DeleteLabel(f.ctx, subj, f.project, idgenerator.NewUUIDv7())
		},
	}

	allowed := map[string]authz.Subject{"operator": operator(), "manager": manager()}
	refused := map[string]authz.Subject{
		"member":              member(),
		"signed in, no grant": stranger(),
		"anonymous":           {},
	}

	for name, op := range operations {
		for who, subj := range refused {
			t.Run(name+"/"+who, func(t *testing.T) {
				f := newService(t)
				laneUUID := lane(t, f)

				err := op(f, subj, laneUUID)

				require.Error(t, err)
				assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code,
					"a %s must not be able to %s", who, name)
			})
		}
		for who, subj := range allowed {
			t.Run(name+"/"+who+" is allowed through the check", func(t *testing.T) {
				f := newService(t)
				laneUUID := lane(t, f)

				err := op(f, subj, laneUUID)

				// The operation may still fail on its own merits — deleting a
				// label that does not exist, for instance. What must not happen
				// is a 403.
				if err != nil {
					assert.NotEqual(t, apperrors.CodeForbidden, apperrors.From(err).Code,
						"a %s should not be refused permission to %s", who, name)
				}
			})
		}
	}
}

func TestAMemberCanStillReadTheBoard(t *testing.T) {
	// The other half of the requirement: a member sees the same board. Refusing
	// them the shape must not refuse them the view.
	f := newService(t)
	_, err := f.svc.CreateLane(f.ctx, operator(), f.project, laneInput("backlog"))
	require.NoError(t, err)

	lanes, err := f.svc.Lanes(f.ctx, member(), f.project)
	require.NoError(t, err)
	assert.Len(t, lanes, 1)

	labels, err := f.svc.Labels(f.ctx, member(), f.project)
	require.NoError(t, err)
	assert.Empty(t, labels)
}

func TestSomebodyWithNoGrantCannotEvenReadTheBoard(t *testing.T) {
	f := newService(t)
	_, err := f.svc.CreateLane(f.ctx, operator(), f.project, laneInput("backlog"))
	require.NoError(t, err)

	_, err = f.svc.Lanes(f.ctx, stranger(), f.project)
	require.Error(t, err)
	assert.Equal(t, apperrors.CodeNotFound, apperrors.From(err).Code,
		"404 rather than 403: a 403 confirms the board exists")
}

func TestCreateLaneAppendsAndSeedsDefaults(t *testing.T) {
	f := newService(t)

	require.NoError(t, f.svc.SeedDefaultLanes(f.ctx, f.project))

	lanes, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)
	require.Len(t, lanes, 5)
	assert.Equal(t, []string{"Backlog", "In progress", "Testing", "Done", "Stash"},
		laneNames(lanes))
	for i, l := range lanes {
		assert.Equal(t, i, l.Position, "positions must be dense and zero-based")
	}
	assert.True(t, lanes[3].IsDone, "Done is marked as a done lane")

	added, err := f.svc.CreateLane(f.ctx, manager(), f.project, laneInput("Blocked"))
	require.NoError(t, err)
	assert.Equal(t, 5, added.Position, "appended to the end")
}

func TestLaneCeiling(t *testing.T) {
	f := newService(t)
	for i := range domain.MaxLanesPerProject {
		_, err := f.svc.CreateLane(f.ctx, manager(), f.project, laneInput("lane"))
		require.NoError(t, err, "lane %d", i+1)
	}

	_, err := f.svc.CreateLane(f.ctx, manager(), f.project, laneInput("one too many"))

	require.Error(t, err)
	assert.ErrorIs(t, err, board.ErrTooManyLanes)
	assert.Equal(t, apperrors.CodeConflict, apperrors.From(err).Code)
}

func TestUpdateLaneDoesNotMoveIt(t *testing.T) {
	// Position changes only through ReorderLanes, so ordering stays dense.
	f := newService(t)
	require.NoError(t, f.svc.SeedDefaultLanes(f.ctx, f.project))
	lanes, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)

	second := lanes[1]
	limit := 3
	updated, err := f.svc.UpdateLane(f.ctx, manager(), f.project, second.UUID, board.LaneInput{
		Name: "Doing now", Colour: domain.ColourTeal, WIPLimit: &limit, IsDone: false,
	})
	require.NoError(t, err)

	assert.Equal(t, "Doing now", updated.Name)
	assert.Equal(t, domain.ColourTeal, updated.Colour)
	assert.Equal(t, 3, updated.Limit())
	assert.Equal(t, second.Position, updated.Position, "an edit must not move the lane")

	after, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)
	assert.Equal(t, laneNames(lanes)[0], laneNames(after)[0], "order unchanged")
}

func TestClearingAWIPLimit(t *testing.T) {
	f := newService(t)
	limit := 4
	lane, err := f.svc.CreateLane(f.ctx, manager(), f.project, board.LaneInput{
		Name: "Doing", Colour: domain.ColourSlate, WIPLimit: &limit,
	})
	require.NoError(t, err)
	require.Equal(t, 4, lane.Limit())

	cleared, err := f.svc.UpdateLane(f.ctx, manager(), f.project, lane.UUID, board.LaneInput{
		Name: "Doing", Colour: domain.ColourSlate, WIPLimit: nil,
	})
	require.NoError(t, err)

	assert.Nil(t, cleared.WIPLimit, "an empty field means no limit, which is NULL")
	assert.Zero(t, cleared.Limit())
}

func TestALaneFromAnotherBoardIsInvisible(t *testing.T) {
	// The projectUUID scope is the authorisation: naming another board's lane
	// resolves to nothing rather than to a permission error.
	f := newService(t)
	other := idgenerator.NewUUIDv7()
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO projects (uuid, slug, name, created_by, created_at, updated_at)
		VALUES (:uuid, 'other', 'Other', :owner, :now, :now)`,
		map[string]any{"uuid": other, "owner": f.owner, "now": database.Now()})
	require.NoError(t, err)

	theirs, err := f.svc.CreateLane(f.ctx, operator(), other, laneInput("theirs"))
	require.NoError(t, err)

	t.Run("read", func(t *testing.T) {
		_, err := f.svc.Lane(f.ctx, manager(), f.project, theirs.UUID)
		assert.ErrorIs(t, err, board.ErrNoSuchLane)
	})
	t.Run("update", func(t *testing.T) {
		_, err := f.svc.UpdateLane(f.ctx, manager(), f.project, theirs.UUID, laneInput("hijacked"))
		assert.ErrorIs(t, err, board.ErrNoSuchLane)
	})
	t.Run("reorder", func(t *testing.T) {
		mine, err := f.svc.CreateLane(f.ctx, manager(), f.project, laneInput("mine"))
		require.NoError(t, err)
		err = f.svc.ReorderLanes(f.ctx, manager(), f.project, []string{theirs.UUID, mine.UUID})
		require.Error(t, err, "an order naming a foreign lane must be refused")
	})

	// And it is untouched.
	still, err := f.svc.Lane(f.ctx, operator(), other, theirs.UUID)
	require.NoError(t, err)
	assert.Equal(t, "theirs", still.Name)
}

func TestReorderLanes(t *testing.T) {
	f := newService(t)
	require.NoError(t, f.svc.SeedDefaultLanes(f.ctx, f.project))
	lanes, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)

	reversed := make([]string, 0, len(lanes))
	for i := len(lanes) - 1; i >= 0; i-- {
		reversed = append(reversed, lanes[i].UUID)
	}

	require.NoError(t, f.svc.ReorderLanes(f.ctx, manager(), f.project, reversed))

	after, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)
	assert.Equal(t, []string{"Stash", "Done", "Testing", "In progress", "Backlog"},
		laneNames(after))
	for i, l := range after {
		assert.Equal(t, i, l.Position)
	}
}

func TestReorderMustNameEveryLane(t *testing.T) {
	// A partial order would leave the unnamed lanes with positions that collide
	// with the new ones.
	f := newService(t)
	require.NoError(t, f.svc.SeedDefaultLanes(f.ctx, f.project))
	lanes, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)

	err = f.svc.ReorderLanes(f.ctx, manager(), f.project,
		[]string{lanes[0].UUID, lanes[1].UUID})

	require.Error(t, err)
	assert.Equal(t, apperrors.CodeInvalid, apperrors.From(err).Code)

	after, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)
	assert.Equal(t, laneNames(lanes), laneNames(after), "a refused reorder changes nothing")
}

func TestABoardNeedsAtLeastOneLane(t *testing.T) {
	f := newService(t)
	only, err := f.svc.CreateLane(f.ctx, manager(), f.project, laneInput("only"))
	require.NoError(t, err)

	err = f.svc.DeleteLane(f.ctx, manager(), f.project, only.UUID, "")

	require.Error(t, err)
	assert.Equal(t, apperrors.CodeConflict, apperrors.From(err).Code)

	lanes, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)
	assert.Len(t, lanes, 1, "the last lane must survive")
}

func TestDeleteLaneMovesItsCards(t *testing.T) {
	// Deleting a lane that holds cards requires saying where they go. The store
	// test covers the SQL; this covers the service refusing without a target.
	f := newService(t)
	require.NoError(t, f.svc.SeedDefaultLanes(f.ctx, f.project))
	lanes, err := f.svc.Lanes(f.ctx, operator(), f.project)
	require.NoError(t, err)
	from, to := lanes[0], lanes[1]

	_, err = f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO cards (uuid, project_uuid, lane_uuid, title, position,
		                   created_by, created_at, updated_at)
		VALUES (:uuid, :project, :lane, 'a card', 0, :owner, :now, :now)`,
		map[string]any{
			"uuid": idgenerator.NewUUIDv7(), "project": f.project, "lane": from.UUID,
			"owner": f.owner, "now": database.Now(),
		})
	require.NoError(t, err)

	t.Run("refused with nowhere to put them", func(t *testing.T) {
		err := f.svc.DeleteLane(f.ctx, manager(), f.project, from.UUID, "")
		require.Error(t, err)
		assert.Equal(t, apperrors.CodeConflict, apperrors.From(err).Code)

		_, err = f.svc.Lane(f.ctx, manager(), f.project, from.UUID)
		assert.NoError(t, err, "the lane survives a refused delete")
	})

	t.Run("accepted with a target", func(t *testing.T) {
		require.NoError(t, f.svc.DeleteLane(f.ctx, manager(), f.project, from.UUID, to.UUID))

		_, err := f.svc.Lane(f.ctx, manager(), f.project, from.UUID)
		assert.ErrorIs(t, err, board.ErrNoSuchLane)

		var moved int
		require.NoError(t, f.db.Reader().GetContext(f.ctx, &moved,
			`SELECT count(*) FROM cards WHERE lane_uuid = ?`, to.UUID))
		assert.Equal(t, 1, moved, "the card went to the target lane")
	})
}

func TestLabelLifecycle(t *testing.T) {
	f := newService(t)

	label, err := f.svc.CreateLabel(f.ctx, manager(), f.project,
		board.LabelInput{Name: "bug", Colour: domain.ColourRose})
	require.NoError(t, err)

	renamed, err := f.svc.UpdateLabel(f.ctx, manager(), f.project, label.UUID,
		board.LabelInput{Name: "defect", Colour: domain.ColourAmber})
	require.NoError(t, err)
	assert.Equal(t, "defect", renamed.Name)
	assert.Equal(t, domain.ColourAmber, renamed.Colour)

	require.NoError(t, f.svc.DeleteLabel(f.ctx, manager(), f.project, label.UUID))

	labels, err := f.svc.Labels(f.ctx, member(), f.project)
	require.NoError(t, err)
	assert.Empty(t, labels)
}

func TestSeedDefaultLanesIsIdempotentlyOrdered(t *testing.T) {
	// Called inside project creation, so it must produce the same order every
	// time — the board's shape is what a new user first sees.
	for range 3 {
		f := newService(t)
		require.NoError(t, f.svc.SeedDefaultLanes(f.ctx, f.project))
		lanes, err := f.svc.Lanes(f.ctx, operator(), f.project)
		require.NoError(t, err)
		assert.Equal(t, []string{"Backlog", "In progress", "Testing", "Done", "Stash"},
			laneNames(lanes))
	}
}

func TestServiceClockIsInjectable(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	f := newService(t)
	f.svc.WithClock(func() time.Time { return at })

	lane, err := f.svc.CreateLane(f.ctx, manager(), f.project, laneInput("timed"))
	require.NoError(t, err)
	assert.True(t, at.Equal(lane.CreatedAt))
}

func laneNames(lanes []*domain.Lane) []string {
	out := make([]string, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, l.Name)
	}
	return out
}
