package card_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/board"
	boarddomain "github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/card"
	"github.com/AzmainMahtab/chonkboard/internal/card/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	projectdomain "github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// The card service is wired against the real board service rather than a fake: the
// two are genuinely coupled through lane ownership, and a fake lane reader would let
// a bug in that coupling through.
type serviceFixture struct {
	svc     *card.Service
	board   *board.Service
	db      *database.DB
	ctx     context.Context
	project string
	owner   *authdomain.User
	member  *authdomain.User
	lanes   []*boarddomain.Lane
}

func newService(t *testing.T) *serviceFixture {
	t.Helper()
	db, tx := dbtest.NewWithTx(t)
	ctx := context.Background()
	log := dbtest.Discard()

	boardSvc := board.NewService(board.NewStore(tx), tx, log)
	f := &serviceFixture{
		svc:   card.NewService(card.NewStore(tx), boardSvc, tx, log),
		board: boardSvc,
		db:    db,
		ctx:   ctx,
	}

	f.owner = f.addUser(t, "owner@example.com", authdomain.RoleSuperAdmin)
	f.member = f.addUser(t, "member@example.com", authdomain.RoleMember)

	f.project = idgenerator.NewUUIDv7()
	_, err := db.Writer().NamedExecContext(ctx, `
		INSERT INTO projects (uuid, slug, name, created_by, created_at, updated_at)
		VALUES (:uuid, 'chonk', 'Chonkboard', :owner, :now, :now)`,
		map[string]any{"uuid": f.project, "owner": f.owner.UUID, "now": database.Now()})
	require.NoError(t, err)

	require.NoError(t, boardSvc.SeedDefaultLanes(ctx, f.project))
	f.lanes, err = boardSvc.Lanes(ctx, f.as(f.owner, nil), f.project)
	require.NoError(t, err)
	require.Len(t, f.lanes, 5)
	return f
}

func (f *serviceFixture) addUser(t *testing.T, email string, role authdomain.Role) *authdomain.User {
	t.Helper()
	u := &authdomain.User{
		UUID: idgenerator.NewUUIDv7(), Email: email, DisplayName: email,
		Role: role, Status: authdomain.StatusActive,
	}
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
		VALUES (:uuid, :email, :email, 'hash', :role, :now, :now)`,
		map[string]any{"uuid": u.UUID, "email": email, "role": string(role), "now": database.Now()})
	require.NoError(t, err)
	return u
}

// as builds a subject: nil role for the operator, a grant for anybody else.
func (f *serviceFixture) as(u *authdomain.User, role *projectdomain.ProjectRole) authz.Subject {
	return authz.NewSubject(u, role)
}

func (f *serviceFixture) asMember() authz.Subject {
	role := projectdomain.RoleMember
	return f.as(f.member, &role)
}

func (f *serviceFixture) asManager() authz.Subject {
	role := projectdomain.RoleManager
	return f.as(f.member, &role)
}

func (f *serviceFixture) asOperator() authz.Subject { return f.as(f.owner, nil) }

func (f *serviceFixture) asStranger() authz.Subject {
	return f.as(&authdomain.User{
		UUID: "str", Role: authdomain.RoleMember, Status: authdomain.StatusActive,
	}, nil)
}

func (f *serviceFixture) lane(i int) string { return f.lanes[i].UUID }

// add creates a card and returns it.
func (f *serviceFixture) add(t *testing.T, subj authz.Subject, laneIdx int, title string) *domain.Card {
	t.Helper()
	c, err := f.svc.Create(f.ctx, subj, f.project, f.lane(laneIdx), title)
	require.NoError(t, err)
	return c
}

// order returns the titles of a lane's live cards, asserting density as it goes.
func (f *serviceFixture) order(t *testing.T, laneIdx int) []string {
	t.Helper()
	_, cards, err := f.svc.ForLane(f.ctx, f.asOperator(), f.project, f.lane(laneIdx))
	require.NoError(t, err)

	titles := make([]string, 0, len(cards))
	for i, c := range cards {
		assert.Equal(t, i, c.Position, "positions must be dense and zero-based")
		titles = append(titles, c.Title)
	}
	return titles
}

func (f *serviceFixture) setWIP(t *testing.T, laneIdx, limit int) {
	t.Helper()
	l := f.lanes[laneIdx]
	_, err := f.board.UpdateLane(f.ctx, f.asManager(), f.project, l.UUID, board.LaneInput{
		Name: l.Name, Colour: l.Colour, WIPLimit: &limit, IsDone: l.IsDone,
	})
	require.NoError(t, err)
}

func TestCreateAppendsToTheLane(t *testing.T) {
	f := newService(t)
	for _, title := range []string{"first", "second", "third"} {
		f.add(t, f.asMember(), 0, title)
	}
	assert.Equal(t, []string{"first", "second", "third"}, f.order(t, 0))
}

func TestAMemberMayCreateEditAndMove(t *testing.T) {
	// The brief verbatim: this is what a member is here to do, and it must work on
	// anybody's card, not only their own.
	f := newService(t)
	theirs := f.add(t, f.asOperator(), 0, "the owner's card")

	edited, err := f.svc.Edit(f.ctx, f.asMember(), f.project, theirs.UUID,
		card.EditInput{Title: "edited by a member", Description: "and described"})
	require.NoError(t, err)
	assert.Equal(t, "edited by a member", edited.Title)

	affected, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
		CardUUID: theirs.UUID, ToLane: f.lane(1), ToOrder: []string{theirs.UUID},
		FromLane: f.lane(0), FromOrder: nil,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{f.lane(0), f.lane(1)}, affected)
	assert.Equal(t, []string{"edited by a member"}, f.order(t, 1))
}

func TestSomebodyWithNoGrantCanDoNothing(t *testing.T) {
	f := newService(t)
	existing := f.add(t, f.asOperator(), 0, "a card")
	stranger := f.asStranger()

	_, err := f.svc.Create(f.ctx, stranger, f.project, f.lane(0), "theirs")
	assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)

	_, err = f.svc.ByUUID(f.ctx, stranger, f.project, existing.UUID)
	assert.ErrorIs(t, err, card.ErrNoSuchCard)

	_, err = f.svc.Edit(f.ctx, stranger, f.project, existing.UUID, card.EditInput{Title: "x"})
	assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)

	_, err = f.svc.Move(f.ctx, stranger, f.project, domain.Move{
		CardUUID: existing.UUID, ToLane: f.lane(1), ToOrder: []string{existing.UUID},
	})
	assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)
}

func TestACardFromAnotherBoardIsInvisible(t *testing.T) {
	f := newService(t)

	other := idgenerator.NewUUIDv7()
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO projects (uuid, slug, name, created_by, created_at, updated_at)
		VALUES (:uuid, 'other', 'Other', :owner, :now, :now)`,
		map[string]any{"uuid": other, "owner": f.owner.UUID, "now": database.Now()})
	require.NoError(t, err)
	require.NoError(t, f.board.SeedDefaultLanes(f.ctx, other))
	otherLanes, err := f.board.Lanes(f.ctx, f.asOperator(), other)
	require.NoError(t, err)

	theirs, err := f.svc.Create(f.ctx, f.asOperator(), other, otherLanes[0].UUID, "theirs")
	require.NoError(t, err)
	mine := f.add(t, f.asOperator(), 0, "mine")

	t.Run("read", func(t *testing.T) {
		_, err := f.svc.ByUUID(f.ctx, f.asMember(), f.project, theirs.UUID)
		assert.ErrorIs(t, err, card.ErrNoSuchCard)
	})
	t.Run("edit", func(t *testing.T) {
		_, err := f.svc.Edit(f.ctx, f.asMember(), f.project, theirs.UUID, card.EditInput{Title: "x"})
		assert.ErrorIs(t, err, card.ErrNoSuchCard)
	})
	t.Run("delete", func(t *testing.T) {
		_, err := f.svc.Delete(f.ctx, f.asManager(), f.project, theirs.UUID)
		assert.ErrorIs(t, err, card.ErrNoSuchCard)
	})
	t.Run("move it onto my board", func(t *testing.T) {
		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: theirs.UUID, ToLane: f.lane(0),
			ToOrder: []string{mine.UUID, theirs.UUID},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrCardNotOnBoard)
	})
	t.Run("name it in my order", func(t *testing.T) {
		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: mine.UUID, ToLane: f.lane(0),
			ToOrder: []string{theirs.UUID, mine.UUID},
		})
		require.Error(t, err)
	})
	t.Run("move mine into their lane", func(t *testing.T) {
		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: mine.UUID, ToLane: otherLanes[0].UUID, ToOrder: []string{mine.UUID},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrLaneNotOnBoard)
	})

	// Both boards untouched.
	assert.Equal(t, []string{"mine"}, f.order(t, 0))
	_, theirCards, err := f.svc.ForLane(f.ctx, f.asOperator(), other, otherLanes[0].UUID)
	require.NoError(t, err)
	require.Len(t, theirCards, 1)
	assert.Equal(t, "theirs", theirCards[0].Title)
}

func TestCreateIntoALaneFromAnotherBoardIsRefused(t *testing.T) {
	f := newService(t)
	_, err := f.svc.Create(f.ctx, f.asMember(), f.project, idgenerator.NewUUIDv7(), "orphan")
	require.Error(t, err)
	assert.Equal(t, apperrors.CodeNotFound, apperrors.From(err).Code)
}

// TestMoveCases is the gate: every shape a drag can take, and what the board looks
// like afterwards.
func TestMoveCases(t *testing.T) {
	t.Run("reorder in place", func(t *testing.T) {
		f := newService(t)
		a := f.add(t, f.asMember(), 0, "a")
		b := f.add(t, f.asMember(), 0, "b")
		c := f.add(t, f.asMember(), 0, "c")

		affected, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: c.UUID, ToLane: f.lane(0),
			ToOrder: []string{c.UUID, a.UUID, b.UUID},
		})

		require.NoError(t, err)
		assert.Equal(t, []string{f.lane(0)}, affected, "only one lane changed")
		assert.Equal(t, []string{"c", "a", "b"}, f.order(t, 0))
	})

	t.Run("across lanes", func(t *testing.T) {
		f := newService(t)
		a := f.add(t, f.asMember(), 0, "a")
		b := f.add(t, f.asMember(), 0, "b")
		resident := f.add(t, f.asMember(), 1, "resident")

		affected, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: a.UUID, ToLane: f.lane(1),
			ToOrder:  []string{resident.UUID, a.UUID},
			FromLane: f.lane(0), FromOrder: []string{b.UUID},
		})

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{f.lane(0), f.lane(1)}, affected)
		assert.Equal(t, []string{"b"}, f.order(t, 0))
		assert.Equal(t, []string{"resident", "a"}, f.order(t, 1))
	})

	t.Run("into an empty lane, emptying the source", func(t *testing.T) {
		f := newService(t)
		only := f.add(t, f.asMember(), 0, "only")

		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: only.UUID, ToLane: f.lane(2), ToOrder: []string{only.UUID},
			FromLane: f.lane(0), FromOrder: nil,
		})

		require.NoError(t, err)
		assert.Empty(t, f.order(t, 0))
		assert.Equal(t, []string{"only"}, f.order(t, 2))
	})

	t.Run("a WIP breach changes nothing", func(t *testing.T) {
		f := newService(t)
		f.setWIP(t, 1, 2)
		moving := f.add(t, f.asMember(), 0, "moving")
		staying := f.add(t, f.asMember(), 0, "staying")
		full1 := f.add(t, f.asMember(), 1, "full-1")
		full2 := f.add(t, f.asMember(), 1, "full-2")

		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: moving.UUID, ToLane: f.lane(1),
			ToOrder:  []string{full1.UUID, full2.UUID, moving.UUID},
			FromLane: f.lane(0), FromOrder: []string{staying.UUID},
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrWIPExceeded)
		assert.Equal(t, apperrors.CodeConflict, apperrors.From(err).Code)
		assert.Equal(t, []string{"moving", "staying"}, f.order(t, 0))
		assert.Equal(t, []string{"full-1", "full-2"}, f.order(t, 1))
	})

	t.Run("a full lane can still be reordered", func(t *testing.T) {
		// The limit gates arrivals; it does not freeze the column.
		f := newService(t)
		f.setWIP(t, 1, 2)
		first := f.add(t, f.asMember(), 1, "first")
		second := f.add(t, f.asMember(), 1, "second")

		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: second.UUID, ToLane: f.lane(1),
			ToOrder: []string{second.UUID, first.UUID},
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"second", "first"}, f.order(t, 1))
	})

	t.Run("a stale from_lane is refused", func(t *testing.T) {
		// Two people dragging at once must not write each other's order.
		f := newService(t)
		c := f.add(t, f.asMember(), 0, "a card")

		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: c.UUID, ToLane: f.lane(1), ToOrder: []string{c.UUID},
			FromLane: f.lane(2), FromOrder: nil, // it is not in lane 2
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrOrderMismatch)
		assert.Equal(t, []string{"a card"}, f.order(t, 0))
	})

	t.Run("an order missing the moved card is refused", func(t *testing.T) {
		f := newService(t)
		a := f.add(t, f.asMember(), 0, "a")
		b := f.add(t, f.asMember(), 0, "b")

		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: a.UUID, ToLane: f.lane(0), ToOrder: []string{b.UUID},
		})

		assert.ErrorIs(t, err, domain.ErrCardMissing)
		assert.Equal(t, []string{"a", "b"}, f.order(t, 0))
	})

	t.Run("the same card twice is refused", func(t *testing.T) {
		f := newService(t)
		a := f.add(t, f.asMember(), 0, "a")

		_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
			CardUUID: a.UUID, ToLane: f.lane(0), ToOrder: []string{a.UUID, a.UUID},
		})

		assert.ErrorIs(t, err, domain.ErrDuplicateInMove)
	})
}

func TestMoveWritesActivityOnlyForALaneChange(t *testing.T) {
	// A reorder inside one lane happens constantly; logging it would drown a card's
	// history in noise.
	f := newService(t)
	a := f.add(t, f.asMember(), 0, "a")
	b := f.add(t, f.asMember(), 0, "b")

	_, err := f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
		CardUUID: b.UUID, ToLane: f.lane(0), ToOrder: []string{b.UUID, a.UUID},
	})
	require.NoError(t, err)

	history, err := f.svc.History(f.ctx, f.asMember(), f.project, b.UUID, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, card.ActivityCreated, history[0].Kind, "the reorder was not logged")

	_, err = f.svc.Move(f.ctx, f.asMember(), f.project, domain.Move{
		CardUUID: b.UUID, ToLane: f.lane(1), ToOrder: []string{b.UUID},
		FromLane: f.lane(0), FromOrder: []string{a.UUID},
	})
	require.NoError(t, err)

	history, err = f.svc.History(f.ctx, f.asMember(), f.project, b.UUID, 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, card.ActivityMoved, history[0].Kind, "newest first")
	require.NotNil(t, history[0].FromLaneUUID)
	assert.Equal(t, f.lane(0), *history[0].FromLaneUUID)
	require.NotNil(t, history[0].ToLaneUUID)
	assert.Equal(t, f.lane(1), *history[0].ToLaneUUID)
}

func TestActivityIsWrittenForEveryChange(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	_, err := f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID,
		card.EditInput{Title: "edited"})
	require.NoError(t, err)
	_, err = f.svc.SetArchived(f.ctx, f.asMember(), f.project, c.UUID, true)
	require.NoError(t, err)
	_, err = f.svc.SetArchived(f.ctx, f.asMember(), f.project, c.UUID, false)
	require.NoError(t, err)

	history, err := f.svc.History(f.ctx, f.asMember(), f.project, c.UUID, 10)
	require.NoError(t, err)

	kinds := make([]card.ActivityKind, 0, len(history))
	for _, h := range history {
		kinds = append(kinds, h.Kind)
	}
	assert.Equal(t, []card.ActivityKind{
		card.ActivityRestored, card.ActivityArchived,
		card.ActivityUpdated, card.ActivityCreated,
	}, kinds, "newest first")
}

func TestArchivedCardsLeaveTheBoardAndTheLaneStaysDense(t *testing.T) {
	f := newService(t)
	first := f.add(t, f.asMember(), 0, "first")
	f.add(t, f.asMember(), 0, "second")
	f.add(t, f.asMember(), 0, "third")

	_, err := f.svc.SetArchived(f.ctx, f.asMember(), f.project, first.UUID, true)
	require.NoError(t, err)

	assert.Equal(t, []string{"second", "third"}, f.order(t, 0))

	byLane, err := f.svc.ForProject(f.ctx, f.asMember(), f.project)
	require.NoError(t, err)
	assert.Len(t, byLane[f.lane(0)], 2, "the board shows live cards only")

	// Still reachable directly, so it can be restored.
	archived, err := f.svc.ByUUID(f.ctx, f.asMember(), f.project, first.UUID)
	require.NoError(t, err)
	assert.True(t, archived.IsArchived())

	_, err = f.svc.SetArchived(f.ctx, f.asMember(), f.project, first.UUID, false)
	require.NoError(t, err)
	assert.Len(t, f.order(t, 0), 3, "and the lane is dense again")
}

func TestDeleteLeavesTheLaneDense(t *testing.T) {
	f := newService(t)
	f.add(t, f.asMember(), 0, "first")
	middle := f.add(t, f.asMember(), 0, "middle")
	f.add(t, f.asMember(), 0, "last")

	_, err := f.svc.Delete(f.ctx, f.asManager(), f.project, middle.UUID)
	require.NoError(t, err)

	// order() asserts density itself.
	assert.Equal(t, []string{"first", "last"}, f.order(t, 0))
}

func TestAMemberMayDeleteOnlyTheirOwnCard(t *testing.T) {
	// The one card operation where ownership matters.
	f := newService(t)
	theirs := f.add(t, f.asMember(), 0, "the member's card")
	somebodyElses := f.add(t, f.asOperator(), 0, "the owner's card")

	_, err := f.svc.Delete(f.ctx, f.asMember(), f.project, somebodyElses.UUID)
	require.Error(t, err)
	assert.ErrorIs(t, err, card.ErrNotYours)
	assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)

	_, err = f.svc.Delete(f.ctx, f.asMember(), f.project, theirs.UUID)
	assert.NoError(t, err, "their own is allowed")

	// A manager may delete anybody's card on their board.
	_, err = f.svc.Delete(f.ctx, f.asManager(), f.project, somebodyElses.UUID)
	assert.NoError(t, err)
}

func TestDeleteTakesTheCardsChildren(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "going")

	_, err := f.svc.Delete(f.ctx, f.asManager(), f.project, c.UUID)
	require.NoError(t, err)

	var activity int
	require.NoError(t, f.db.Reader().GetContext(f.ctx, &activity,
		`SELECT count(*) FROM card_activity WHERE card_uuid = ?`, c.UUID))
	assert.Zero(t, activity, "activity cascades with the card")
}

func TestEditRefusesABadTitle(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "original")

	_, err := f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID, card.EditInput{Title: "   "})

	require.Error(t, err)
	assert.Equal(t, apperrors.CodeValidation, apperrors.From(err).Code)
	require.NotEmpty(t, apperrors.From(err).Fields)
	assert.Equal(t, "title", apperrors.From(err).Fields[0].Field)

	unchanged, err := f.svc.ByUUID(f.ctx, f.asMember(), f.project, c.UUID)
	require.NoError(t, err)
	assert.Equal(t, "original", unchanged.Title)
}

func TestCreateRefusesABadTitle(t *testing.T) {
	f := newService(t)
	_, err := f.svc.Create(f.ctx, f.asMember(), f.project, f.lane(0), "")
	require.Error(t, err)
	assert.Equal(t, apperrors.CodeValidation, apperrors.From(err).Code)
	assert.Empty(t, f.order(t, 0), "nothing was written")
}

func TestForProjectGroupsByLane(t *testing.T) {
	f := newService(t)
	f.add(t, f.asMember(), 0, "backlog one")
	f.add(t, f.asMember(), 0, "backlog two")
	f.add(t, f.asMember(), 1, "doing one")

	byLane, err := f.svc.ForProject(f.ctx, f.asMember(), f.project)
	require.NoError(t, err)

	assert.Len(t, byLane[f.lane(0)], 2)
	assert.Len(t, byLane[f.lane(1)], 1)
	assert.Empty(t, byLane[f.lane(2)])
}

func TestProjectOfIsUnscopedButOnlyYieldsAUUID(t *testing.T) {
	// It is the lookup that enables scoping, so it must find a card on any board —
	// and it must refuse one that does not exist.
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	got, err := f.svc.ProjectOf(f.ctx, c.UUID)
	require.NoError(t, err)
	assert.Equal(t, f.project, got)

	_, err = f.svc.ProjectOf(f.ctx, idgenerator.NewUUIDv7())
	assert.ErrorIs(t, err, card.ErrNoSuchCard)
}

func TestServiceClockIsInjectable(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	f := newService(t)
	f.svc.WithClock(func() time.Time { return at })

	c := f.add(t, f.asMember(), 0, "timed")
	assert.True(t, at.Equal(c.CreatedAt))
}
