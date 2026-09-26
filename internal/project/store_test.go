package project_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// seedUser inserts a user with raw SQL rather than through the auth store: a
// project store test should not depend on another slice's code.
func seedUser(t *testing.T, db *database.DB, email string) string {
	t.Helper()
	uuid := idgenerator.NewUUIDv7()
	now := database.Now()
	_, err := db.Writer().NamedExecContext(context.Background(), `
		INSERT INTO users (uuid, email, display_name, password_hash, role,
		                   created_at, updated_at)
		VALUES (:uuid, :email, 'Test Person', 'hash', 'member', :now, :now)`,
		map[string]any{"uuid": uuid, "email": email, "now": now})
	require.NoError(t, err)
	return uuid
}

func newStore(t *testing.T) (*project.Store, *database.DB, context.Context) {
	t.Helper()
	db, tx := dbtest.NewWithTx(t)
	return project.NewStore(tx), db, context.Background()
}

func makeProject(t *testing.T, slug, name, createdBy string) *domain.Project {
	t.Helper()
	p, err := domain.NewProject(idgenerator.NewUUIDv7(), slug, name, "", createdBy, time.Now())
	require.NoError(t, err)
	return p
}

func TestCreateAndLoadProject(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	want := makeProject(t, "chonk", "Chonkboard", owner)
	require.NoError(t, store.Create(ctx, want))

	byUUID, err := store.ByUUID(ctx, want.UUID)
	require.NoError(t, err)
	require.NotNil(t, byUUID)
	assert.Equal(t, "chonk", byUUID.Slug)
	assert.Equal(t, "Chonkboard", byUUID.Name)
	assert.False(t, byUUID.IsArchived())

	bySlug, err := store.BySlug(ctx, "CHONK")
	require.NoError(t, err)
	require.NotNil(t, bySlug, "slug lookup normalises case")
	assert.Equal(t, want.UUID, bySlug.UUID)

	absent, err := store.ByUUID(ctx, idgenerator.NewUUIDv7())
	assert.NoError(t, err)
	assert.Nil(t, absent)
}

func TestDuplicateSlugIsAFieldError(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	require.NoError(t, store.Create(ctx, makeProject(t, "chonk", "First", owner)))

	err := store.Create(ctx, makeProject(t, "chonk", "Second", owner))

	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeConflict, ""))
	require.Len(t, apperrors.From(err).Fields, 1)
	assert.Equal(t, "slug", apperrors.From(err).Fields[0].Field)
}

func TestProjectForAnAbsentCreatorIsRefused(t *testing.T) {
	store, _, ctx := newStore(t)
	err := store.Create(ctx, makeProject(t, "ghost", "Ghost", idgenerator.NewUUIDv7()))
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
}

func TestArchiveHidesFromTheDefaultListing(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	live := makeProject(t, "live", "Live", owner)
	stashed := makeProject(t, "stashed", "Stashed", owner)
	require.NoError(t, store.Create(ctx, live))
	require.NoError(t, store.Create(ctx, stashed))

	stashed.Archive(time.Now())
	require.NoError(t, store.Update(ctx, stashed))

	visible, err := store.ListAll(ctx, false)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	assert.Equal(t, "live", visible[0].Slug)

	all, err := store.ListAll(ctx, true)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	reloaded, err := store.ByUUID(ctx, stashed.UUID)
	require.NoError(t, err)
	assert.True(t, reloaded.IsArchived())

	reloaded.Restore(time.Now())
	require.NoError(t, store.Update(ctx, reloaded))
	visible, err = store.ListAll(ctx, false)
	require.NoError(t, err)
	assert.Len(t, visible, 2)
}

func TestListForUserReturnsOnlyGrantedProjects(t *testing.T) {
	// The access rule is the join, applied in the query rather than filtered
	// afterwards: a project with no grant never reaches Go, so no code path can
	// forget to check it.
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	member := seedUser(t, db, "member@example.com")

	granted := makeProject(t, "granted", "Granted", owner)
	other := makeProject(t, "other", "Other", owner)
	require.NoError(t, store.Create(ctx, granted))
	require.NoError(t, store.Create(ctx, other))

	m, err := domain.NewMembership(granted.UUID, member, domain.RoleMember, owner, time.Now())
	require.NoError(t, err)
	require.NoError(t, store.Grant(ctx, m))

	mine, err := store.ListForUser(ctx, member, false)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	assert.Equal(t, "granted", mine[0].Slug)

	none, err := store.ListForUser(ctx, seedUser(t, db, "stranger@example.com"), false)
	require.NoError(t, err)
	assert.Empty(t, none, "no grant means no projects, not all projects")
}

func TestGrantIsAnUpsert(t *testing.T) {
	// Re-granting to change a role is the common case. Making it an error would
	// push a read-then-write race into the caller.
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	person := seedUser(t, db, "person@example.com")
	p := makeProject(t, "chonk", "Chonkboard", owner)
	require.NoError(t, store.Create(ctx, p))

	asMember, err := domain.NewMembership(p.UUID, person, domain.RoleMember, owner, time.Now())
	require.NoError(t, err)
	require.NoError(t, store.Grant(ctx, asMember))

	asManager, err := domain.NewMembership(p.UUID, person, domain.RoleManager, owner, time.Now())
	require.NoError(t, err)
	require.NoError(t, store.Grant(ctx, asManager))

	got, err := store.Membership(ctx, p.UUID, person)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, domain.RoleManager, got.Role)
	assert.True(t, got.Role.CanManageBoard())

	members, err := store.Members(ctx, p.UUID)
	require.NoError(t, err)
	assert.Len(t, members, 1, "the upsert must not have created a second row")
}

func TestMembershipAbsenceIsDenial(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	p := makeProject(t, "chonk", "Chonkboard", owner)
	require.NoError(t, store.Create(ctx, p))

	got, err := store.Membership(ctx, p.UUID, seedUser(t, db, "stranger@example.com"))
	assert.NoError(t, err)
	assert.Nil(t, got, "absence is denial; there is no negative grant")
}

func TestGrantToNobodyIsRefused(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	p := makeProject(t, "chonk", "Chonkboard", owner)
	require.NoError(t, store.Create(ctx, p))

	m, err := domain.NewMembership(p.UUID, idgenerator.NewUUIDv7(), domain.RoleMember, owner, time.Now())
	require.NoError(t, err)

	err = store.Grant(ctx, m)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
}

func TestRevokeAndCountManagers(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	manager := seedUser(t, db, "manager@example.com")
	member := seedUser(t, db, "member@example.com")
	p := makeProject(t, "chonk", "Chonkboard", owner)
	require.NoError(t, store.Create(ctx, p))

	for uuid, role := range map[string]domain.ProjectRole{
		manager: domain.RoleManager,
		member:  domain.RoleMember,
	} {
		m, err := domain.NewMembership(p.UUID, uuid, role, owner, time.Now())
		require.NoError(t, err)
		require.NoError(t, store.Grant(ctx, m))
	}

	n, err := store.CountManagers(ctx, p.UUID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "callers check this before demoting the last manager")

	require.NoError(t, store.Revoke(ctx, p.UUID, member))
	gone, err := store.Membership(ctx, p.UUID, member)
	require.NoError(t, err)
	assert.Nil(t, gone)

	err = store.Revoke(ctx, p.UUID, member)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""),
		"revoking a grant that is not there is a not-found, not a silent success")
}

func TestDeletingAProjectTakesItsGrants(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	person := seedUser(t, db, "person@example.com")
	p := makeProject(t, "chonk", "Chonkboard", owner)
	require.NoError(t, store.Create(ctx, p))

	m, err := domain.NewMembership(p.UUID, person, domain.RoleMember, owner, time.Now())
	require.NoError(t, err)
	require.NoError(t, store.Grant(ctx, m))

	require.NoError(t, store.Delete(ctx, p.UUID))

	members, err := store.Members(ctx, p.UUID)
	require.NoError(t, err)
	assert.Empty(t, members, "grants cascade with the project")

	assert.ErrorIs(t, store.Delete(ctx, p.UUID), apperrors.New(apperrors.CodeNotFound, ""))
}

func TestUpdateAbsentProjectIsNotFound(t *testing.T) {
	store, db, ctx := newStore(t)
	owner := seedUser(t, db, "owner@example.com")
	err := store.Update(ctx, makeProject(t, "ghost", "Ghost", owner))
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))
}
