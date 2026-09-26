package project_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// fakeDirectory stands in for the auth service. In-memory, because this slice only
// needs names for uuids and wiring a real auth service would drag Argon2id in.
type fakeDirectory struct {
	users []*authdomain.User
}

func (d *fakeDirectory) UserByUUID(_ context.Context, uuid string) (*authdomain.User, error) {
	for _, u := range d.users {
		if u.UUID == uuid {
			return u, nil
		}
	}
	return nil, nil
}

func (d *fakeDirectory) ListUsers(context.Context) ([]*authdomain.User, error) {
	return d.users, nil
}

func (d *fakeDirectory) add(uuid, email string, role authdomain.Role) *authdomain.User {
	u := &authdomain.User{
		UUID: uuid, Email: email, DisplayName: email,
		Role: role, Status: authdomain.StatusActive,
	}
	d.users = append(d.users, u)
	return u
}

// fakeSeeder records which projects were given starting lanes.
type fakeSeeder struct{ seeded []string }

func (s *fakeSeeder) SeedDefaultLanes(_ context.Context, projectUUID string) error {
	s.seeded = append(s.seeded, projectUUID)
	return nil
}

type serviceFixture struct {
	svc    *project.Service
	dir    *fakeDirectory
	seeder *fakeSeeder
	db     *database.DB
	ctx    context.Context
	owner  *authdomain.User
}

func newService(t *testing.T) *serviceFixture {
	t.Helper()
	db, tx := dbtest.NewWithTx(t)
	dir := &fakeDirectory{}
	seeder := &fakeSeeder{}

	f := &serviceFixture{
		svc:    project.NewService(project.NewStore(tx), dir, seeder, tx, dbtest.Discard()),
		dir:    dir,
		seeder: seeder,
		db:     db,
		ctx:    context.Background(),
	}
	f.owner = f.addUser(t, "owner@example.com", authdomain.RoleSuperAdmin)
	return f
}

// addUser inserts an account with raw SQL and registers it with the directory: the
// rows have to exist for the foreign keys, and the directory is what the service
// reads names from.
func (f *serviceFixture) addUser(t *testing.T, email string, role authdomain.Role) *authdomain.User {
	t.Helper()
	uuid := idgenerator.NewUUIDv7()
	_, err := f.db.Writer().NamedExecContext(f.ctx, `
		INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
		VALUES (:uuid, :email, :email, 'hash', :role, :now, :now)`,
		map[string]any{"uuid": uuid, "email": email, "role": string(role), "now": database.Now()})
	require.NoError(t, err)
	return f.dir.add(uuid, email, role)
}

func (f *serviceFixture) suspend(t *testing.T, u *authdomain.User) {
	t.Helper()
	u.Status = authdomain.StatusSuspended
	_, err := f.db.Writer().NamedExecContext(f.ctx,
		`UPDATE users SET status = 'suspended' WHERE uuid = :uuid`,
		map[string]any{"uuid": u.UUID})
	require.NoError(t, err)
}

func (f *serviceFixture) create(t *testing.T, name string) *domain.Project {
	t.Helper()
	p, err := f.svc.Create(f.ctx, f.owner, project.CreateInput{Name: name})
	require.NoError(t, err)
	return p
}

func TestCreateProjectGrantsTheCreatorAndSeedsLanes(t *testing.T) {
	// All three in one transaction: a project whose creator cannot configure it,
	// or one with no lanes, would be immediately broken.
	f := newService(t)

	p, err := f.svc.Create(f.ctx, f.owner, project.CreateInput{
		Name: "  Chonkboard Build  ", Description: "  The board  ",
	})

	require.NoError(t, err)
	assert.Equal(t, "Chonkboard Build", p.Name)
	assert.Equal(t, "chonkboard-build", p.Slug, "derived from the name")
	assert.Equal(t, "The board", p.Description)
	assert.Equal(t, []string{p.UUID}, f.seeder.seeded, "the board got its starting lanes")

	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	require.NotNil(t, access.Role)
	assert.Equal(t, domain.RoleManager, *access.Role, "the creator manages it")
}

func TestCreateProjectWithAnExplicitSlug(t *testing.T) {
	f := newService(t)
	p, err := f.svc.Create(f.ctx, f.owner, project.CreateInput{
		Name: "Quarter Three Roadmap", Slug: "q3",
	})
	require.NoError(t, err)
	assert.Equal(t, "q3", p.Slug)
}

func TestOnlyTheOperatorCanCreateAProject(t *testing.T) {
	f := newService(t)
	member := f.addUser(t, "member@example.com", authdomain.RoleMember)

	_, err := f.svc.Create(f.ctx, member, project.CreateInput{Name: "Theirs"})

	require.Error(t, err)
	assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)
}

func TestCreateProjectRejectsABadSlug(t *testing.T) {
	f := newService(t)
	for _, slug := range []string{"has spaces", "under_score", "double--hyphen", "-leading"} {
		t.Run(slug, func(t *testing.T) {
			_, err := f.svc.Create(f.ctx, f.owner, project.CreateInput{Name: "X", Slug: slug})
			require.Error(t, err)
			assert.Equal(t, apperrors.CodeValidation, apperrors.From(err).Code)
		})
	}
}

func TestDuplicateSlugIsAConflictWithAFieldError(t *testing.T) {
	f := newService(t)
	f.create(t, "Chonkboard")

	_, err := f.svc.Create(f.ctx, f.owner, project.CreateInput{Name: "Other", Slug: "chonkboard"})

	require.Error(t, err)
	assert.Equal(t, apperrors.CodeConflict, apperrors.From(err).Code)
	require.NotEmpty(t, apperrors.From(err).Fields)
	assert.Equal(t, "slug", apperrors.From(err).Fields[0].Field)
}

func TestResolveAcceptsASlugOrAUUID(t *testing.T) {
	// Slugs are what people type and share; uuids are what fragments carry.
	f := newService(t)
	p := f.create(t, "Chonkboard")

	for name, ref := range map[string]string{"slug": p.Slug, "uuid": p.UUID} {
		t.Run(name, func(t *testing.T) {
			access, err := f.svc.Resolve(f.ctx, f.owner, ref)
			require.NoError(t, err)
			assert.Equal(t, p.UUID, access.Project.UUID)
			assert.True(t, access.CanManage())
		})
	}
}

func TestResolveRefusesWithoutAGrant(t *testing.T) {
	// 404, not 403. A 403 on a board somebody has no business knowing about
	// confirms that it exists, which enumerates every project in the installation.
	f := newService(t)
	p := f.create(t, "Chonkboard")
	stranger := f.addUser(t, "stranger@example.com", authdomain.RoleMember)

	_, err := f.svc.Resolve(f.ctx, stranger, p.Slug)

	assert.ErrorIs(t, err, project.ErrNoSuchProject)
	assert.Equal(t, apperrors.CodeNotFound, apperrors.From(err).Code)
}

func TestResolveRefusals(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")

	t.Run("a project that does not exist", func(t *testing.T) {
		_, err := f.svc.Resolve(f.ctx, f.owner, "no-such-board")
		assert.ErrorIs(t, err, project.ErrNoSuchProject)
	})
	t.Run("an empty reference", func(t *testing.T) {
		_, err := f.svc.Resolve(f.ctx, f.owner, "")
		assert.ErrorIs(t, err, project.ErrNoSuchProject)
	})
	t.Run("anonymous", func(t *testing.T) {
		_, err := f.svc.Resolve(f.ctx, nil, p.Slug)
		assert.ErrorIs(t, err, project.ErrNoSuchProject)
	})
	t.Run("a suspended account", func(t *testing.T) {
		f.suspend(t, f.owner)
		_, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
		assert.ErrorIs(t, err, project.ErrNoSuchProject)
	})
}

func TestListIsScopedToGrants(t *testing.T) {
	// The operator sees everything; everybody else sees exactly what they were
	// granted. The scoping is in the query, not a filter afterwards.
	f := newService(t)
	granted := f.create(t, "Granted")
	f.create(t, "Not granted")

	member := f.addUser(t, "member@example.com", authdomain.RoleMember)
	access, err := f.svc.Resolve(f.ctx, f.owner, granted.Slug)
	require.NoError(t, err)
	require.NoError(t, f.svc.Grant(f.ctx, access, member.UUID, domain.RoleMember))

	all, err := f.svc.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	assert.Len(t, all, 2, "the operator sees every board")

	theirs, err := f.svc.List(f.ctx, member, false)
	require.NoError(t, err)
	require.Len(t, theirs, 1)
	assert.Equal(t, "Granted", theirs[0].Project.Name)
	require.NotNil(t, theirs[0].Role)
	assert.Equal(t, domain.RoleMember, *theirs[0].Role)

	stranger := f.addUser(t, "stranger@example.com", authdomain.RoleMember)
	none, err := f.svc.List(f.ctx, stranger, false)
	require.NoError(t, err)
	assert.Empty(t, none, "no grant means no boards, not all boards")
}

func TestArchiveHidesFromEveryList(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)

	require.NoError(t, f.svc.Archive(f.ctx, access))

	visible, err := f.svc.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	assert.Empty(t, visible)

	withArchived, err := f.svc.List(f.ctx, f.owner, true)
	require.NoError(t, err)
	assert.Len(t, withArchived, 1)

	// An archived board is still reachable directly, so it can be restored.
	access, err = f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	assert.True(t, access.Project.IsArchived())

	require.NoError(t, f.svc.Restore(f.ctx, access))
	visible, err = f.svc.List(f.ctx, f.owner, false)
	require.NoError(t, err)
	assert.Len(t, visible, 1)
}

func TestArchiveAndDeleteAreTheOperatorsAlone(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	manager := f.addUser(t, "manager@example.com", authdomain.RoleMember)

	ownerAccess, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	require.NoError(t, f.svc.Grant(f.ctx, ownerAccess, manager.UUID, domain.RoleManager))

	managerAccess, err := f.svc.Resolve(f.ctx, manager, p.Slug)
	require.NoError(t, err)
	require.True(t, managerAccess.CanManage(), "they do manage the board")

	assert.Equal(t, apperrors.CodeForbidden,
		apperrors.From(f.svc.Archive(f.ctx, managerAccess)).Code)
	assert.Equal(t, apperrors.CodeForbidden,
		apperrors.From(f.svc.Delete(f.ctx, managerAccess)).Code)

	// The operator can.
	assert.NoError(t, f.svc.Delete(f.ctx, ownerAccess))
}

func TestRenameIsManagerOrAbove(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	member := f.addUser(t, "member@example.com", authdomain.RoleMember)

	ownerAccess, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	require.NoError(t, f.svc.Grant(f.ctx, ownerAccess, member.UUID, domain.RoleMember))

	memberAccess, err := f.svc.Resolve(f.ctx, member, p.Slug)
	require.NoError(t, err)

	err = f.svc.Rename(f.ctx, memberAccess, "Hijacked", "")
	assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)

	require.NoError(t, f.svc.Rename(f.ctx, ownerAccess, "Renamed", "A description"))
	reloaded, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", reloaded.Project.Name)
	assert.Equal(t, "A description", reloaded.Project.Description)
}

func TestGrantThroughTheServiceIsAnUpsert(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	person := f.addUser(t, "person@example.com", authdomain.RoleMember)
	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)

	require.NoError(t, f.svc.Grant(f.ctx, access, person.UUID, domain.RoleMember))
	require.NoError(t, f.svc.Grant(f.ctx, access, person.UUID, domain.RoleManager))

	members, err := f.svc.Members(f.ctx, access)
	require.NoError(t, err)
	require.Len(t, members, 2, "the owner and the person, not three rows")

	theirs, err := f.svc.Resolve(f.ctx, person, p.Slug)
	require.NoError(t, err)
	assert.Equal(t, domain.RoleManager, *theirs.Role)
}

func TestOnlyTheOperatorCanMintAManager(t *testing.T) {
	// A manager who could mint peers could escalate the board out of the owner's
	// control.
	f := newService(t)
	p := f.create(t, "Chonkboard")
	manager := f.addUser(t, "manager@example.com", authdomain.RoleMember)
	person := f.addUser(t, "person@example.com", authdomain.RoleMember)

	ownerAccess, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	require.NoError(t, f.svc.Grant(f.ctx, ownerAccess, manager.UUID, domain.RoleManager))

	managerAccess, err := f.svc.Resolve(f.ctx, manager, p.Slug)
	require.NoError(t, err)

	t.Run("a manager may grant an ordinary member", func(t *testing.T) {
		assert.NoError(t, f.svc.Grant(f.ctx, managerAccess, person.UUID, domain.RoleMember))
	})
	t.Run("but not another manager", func(t *testing.T) {
		err := f.svc.Grant(f.ctx, managerAccess, person.UUID, domain.RoleManager)
		assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)
	})
	t.Run("and not remove one", func(t *testing.T) {
		err := f.svc.Revoke(f.ctx, managerAccess, f.owner.UUID)
		assert.Equal(t, apperrors.CodeForbidden, apperrors.From(err).Code)
	})
	t.Run("a manager may remove an ordinary member", func(t *testing.T) {
		assert.NoError(t, f.svc.Revoke(f.ctx, managerAccess, person.UUID))
	})
}

func TestAMemberCannotChangeMembership(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	member := f.addUser(t, "member@example.com", authdomain.RoleMember)
	other := f.addUser(t, "other@example.com", authdomain.RoleMember)

	ownerAccess, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	require.NoError(t, f.svc.Grant(f.ctx, ownerAccess, member.UUID, domain.RoleMember))

	memberAccess, err := f.svc.Resolve(f.ctx, member, p.Slug)
	require.NoError(t, err)

	assert.Equal(t, apperrors.CodeForbidden,
		apperrors.From(f.svc.Grant(f.ctx, memberAccess, other.UUID, domain.RoleMember)).Code)
	assert.Equal(t, apperrors.CodeForbidden,
		apperrors.From(f.svc.Revoke(f.ctx, memberAccess, f.owner.UUID)).Code)
}

func TestTheLastManagerCannotBeDemotedOrRemoved(t *testing.T) {
	// Otherwise the board is left with nobody but the operator who can configure
	// it — which is a board somebody has to go and fix.
	f := newService(t)
	p := f.create(t, "Chonkboard")
	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)

	t.Run("demote", func(t *testing.T) {
		err := f.svc.Grant(f.ctx, access, f.owner.UUID, domain.RoleMember)
		assert.ErrorIs(t, err, project.ErrLastManager)
	})
	t.Run("remove", func(t *testing.T) {
		err := f.svc.Revoke(f.ctx, access, f.owner.UUID)
		assert.ErrorIs(t, err, project.ErrLastManager)
	})

	t.Run("allowed once there is a second", func(t *testing.T) {
		second := f.addUser(t, "second@example.com", authdomain.RoleMember)
		require.NoError(t, f.svc.Grant(f.ctx, access, second.UUID, domain.RoleManager))

		assert.NoError(t, f.svc.Grant(f.ctx, access, f.owner.UUID, domain.RoleMember))
	})
}

func TestRevokeSomebodyWhoIsNotAMember(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	stranger := f.addUser(t, "stranger@example.com", authdomain.RoleMember)
	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)

	err = f.svc.Revoke(f.ctx, access, stranger.UUID)
	assert.ErrorIs(t, err, project.ErrNotAMember)
}

func TestGrantToSomebodyWhoDoesNotExist(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)

	err = f.svc.Grant(f.ctx, access, idgenerator.NewUUIDv7(), domain.RoleMember)
	assert.Equal(t, apperrors.CodeNotFound, apperrors.From(err).Code)
}

func TestGrantableExcludesMembersAndSuspendedAccounts(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	already := f.addUser(t, "already@example.com", authdomain.RoleMember)
	suspended := f.addUser(t, "suspended@example.com", authdomain.RoleMember)
	available := f.addUser(t, "available@example.com", authdomain.RoleMember)

	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	require.NoError(t, f.svc.Grant(f.ctx, access, already.UUID, domain.RoleMember))
	f.suspend(t, suspended)

	candidates, err := f.svc.Grantable(f.ctx, access)
	require.NoError(t, err)

	emails := make([]string, 0, len(candidates))
	for _, c := range candidates {
		emails = append(emails, c.Email)
	}
	assert.Equal(t, []string{"available@example.com"}, emails,
		"offering a suspended account would be offering access that does not work")
	_ = available
}

func TestMembersCarryTheirNames(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)

	members, err := f.svc.Members(f.ctx, access)
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "owner@example.com", members[0].User.Email)
	assert.Equal(t, domain.RoleManager, members[0].Membership.Role)
}

func TestDeleteCascades(t *testing.T) {
	f := newService(t)
	p := f.create(t, "Chonkboard")
	person := f.addUser(t, "person@example.com", authdomain.RoleMember)
	access, err := f.svc.Resolve(f.ctx, f.owner, p.Slug)
	require.NoError(t, err)
	require.NoError(t, f.svc.Grant(f.ctx, access, person.UUID, domain.RoleMember))

	require.NoError(t, f.svc.Delete(f.ctx, access))

	_, err = f.svc.Resolve(f.ctx, f.owner, p.Slug)
	assert.ErrorIs(t, err, project.ErrNoSuchProject)

	var grants int
	require.NoError(t, f.db.Reader().GetContext(f.ctx, &grants,
		`SELECT count(*) FROM project_members WHERE project_uuid = ?`, p.UUID))
	assert.Zero(t, grants, "grants cascade with the project")
}

func TestProjectServiceClockIsInjectable(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	f := newService(t)
	f.svc.WithClock(func() time.Time { return at })

	p, err := f.svc.Create(f.ctx, f.owner, project.CreateInput{Name: "Timed"})
	require.NoError(t, err)
	assert.True(t, at.Equal(p.CreatedAt))
}
