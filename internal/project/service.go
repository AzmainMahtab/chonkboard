package project

import (
	"context"
	"log/slog"
	"time"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// The failures this service reports.
var (
	// ErrNoSuchProject is returned both when a project does not exist and when
	// the caller has no grant on it.
	//
	// Deliberately one error. A 403 on a board somebody has no business knowing
	// about confirms that it exists, which is a slow enumeration of every project
	// slug in the installation. A 404 tells them nothing either way.
	ErrNoSuchProject = apperrors.NotFound("No such project.")

	// ErrLastManager refuses a change that would leave a board with nobody who
	// can configure it.
	ErrLastManager = apperrors.Conflict(
		"That is the project's only manager. Promote somebody else first.")

	// ErrNotAMember is for revoking or re-roling a grant that is not there.
	ErrNotAMember = apperrors.NotFound("That person is not a member of this project.")
)

// UserDirectory is the slice of the auth service this one needs: names for uuids.
//
// Declared here, by the consumer, so project depends on two methods rather than on
// the whole auth service.
type UserDirectory interface {
	UserByUUID(ctx context.Context, uuid string) (*authdomain.User, error)
	ListUsers(ctx context.Context) ([]*authdomain.User, error)
}

// BoardSeeder gives a brand-new project its starting lanes.
//
// Declared here because project creation is what needs it; the board slice
// implements it. An empty board is a rail with no affordance, so the lanes are part
// of creating a project rather than a second step that can fail on its own.
type BoardSeeder interface {
	SeedDefaultLanes(ctx context.Context, projectUUID string) error
}

// Service owns projects and who may see them.
type Service struct {
	store *Store
	users UserDirectory
	board BoardSeeder
	tx    *database.TxManager
	log   *slog.Logger
	now   func() time.Time
}

// NewService wires the service.
func NewService(
	store *Store, users UserDirectory, board BoardSeeder,
	tx *database.TxManager, log *slog.Logger,
) *Service {
	return &Service{store: store, users: users, board: board, tx: tx, log: log, now: time.Now}
}

// WithClock replaces the clock. Tests only.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Access is a resolved answer to "may this person touch this board, and how much?".
//
// Every project route starts by getting one. It is the single place a project
// reference from a URL becomes a project plus a permission subject, which is what
// stops a handler from resolving one and forgetting the other.
type Access struct {
	Project *domain.Project
	// Role is the caller's grant, or nil when they have none — which is the
	// normal case for the operator, whose access is global.
	Role    *domain.ProjectRole
	Subject authz.Subject
}

// CanManage reports whether the caller may change the board's shape.
func (a Access) CanManage() bool { return a.Subject.CanManageBoard() }

// Resolve turns a project reference from a URL into an Access, or ErrNoSuchProject.
//
// ref is a slug or a uuid. Slugs are what people type and share; uuids are what
// fragments carry. Accepting both here means no route has to care which it got.
func (s *Service) Resolve(ctx context.Context, user *authdomain.User, ref string) (*Access, error) {
	if user == nil || !user.CanSignIn() {
		return nil, ErrNoSuchProject
	}

	project, err := s.lookup(ctx, ref)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, ErrNoSuchProject
	}

	membership, err := s.store.Membership(ctx, project.UUID, user.UUID)
	if err != nil {
		return nil, err
	}

	var role *domain.ProjectRole
	if membership != nil {
		role = &membership.Role
	}

	access := &Access{
		Project: project,
		Role:    role,
		Subject: authz.NewSubject(user, role),
	}
	if !access.Subject.CanSeeProject() {
		return nil, ErrNoSuchProject
	}
	return access, nil
}

// lookup finds a project by uuid or slug.
//
// A uuid is tried first and only when the reference looks like one, so a slug that
// happens to be 36 characters cannot be misread as an id.
func (s *Service) lookup(ctx context.Context, ref string) (*domain.Project, error) {
	if idgenerator.Valid(ref) {
		return s.store.ByUUID(ctx, ref)
	}
	return s.store.BySlug(ctx, ref)
}

// Summary is a project as the list page shows it.
type Summary struct {
	Project *domain.Project
	// Role is the caller's grant, or nil for an operator with no row.
	Role *domain.ProjectRole
}

// List returns the projects this person may open.
//
// The operator sees every project; everybody else sees exactly what they were
// granted. The scoping happens in the query, not as a filter afterwards, so there
// is no code path that could return an ungranted board.
func (s *Service) List(ctx context.Context, user *authdomain.User, includeArchived bool) ([]Summary, error) {
	if user == nil || !user.CanSignIn() {
		return nil, apperrors.Unauthorized("Please sign in.")
	}

	var projects []*domain.Project
	var err error
	if authz.NewSubject(user, nil).Can(authz.ActionProjectListAll) {
		projects, err = s.store.ListAll(ctx, includeArchived)
	} else {
		projects, err = s.store.ListForUser(ctx, user.UUID, includeArchived)
	}
	if err != nil {
		return nil, err
	}

	// One query for every grant this person holds, rather than one per project.
	roles := make(map[string]domain.ProjectRole, len(projects))
	for _, p := range projects {
		m, err := s.store.Membership(ctx, p.UUID, user.UUID)
		if err != nil {
			return nil, err
		}
		if m != nil {
			roles[p.UUID] = m.Role
		}
	}

	out := make([]Summary, 0, len(projects))
	for _, p := range projects {
		summary := Summary{Project: p}
		if role, ok := roles[p.UUID]; ok {
			r := role
			summary.Role = &r
		}
		out = append(out, summary)
	}
	return out, nil
}

// CreateInput is a new project.
type CreateInput struct {
	Name        string
	Slug        string
	Description string
}

// Create makes a project and grants its creator manager on it.
//
// The project, the creator's manager grant, and the starting lanes all land in one
// transaction. A project whose creator cannot configure it, or one with no lanes,
// would be immediately broken.
func (s *Service) Create(ctx context.Context, user *authdomain.User, in CreateInput) (*domain.Project, error) {
	if !authz.NewSubject(user, nil).Can(authz.ActionProjectCreate) {
		return nil, apperrors.Forbidden("Only the owner can create a project.")
	}

	slug := in.Slug
	if slug == "" {
		// Derived from the name, so the common case is one field.
		slug = domain.SlugFromName(in.Name)
	}

	now := s.now()
	project, err := domain.NewProject(idgenerator.NewUUIDv7(), slug, in.Name, in.Description, user.UUID, now)
	if err != nil {
		return nil, err
	}

	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Create(ctx, project); err != nil {
			return err
		}
		grant, err := domain.NewMembership(project.UUID, user.UUID, domain.RoleManager, user.UUID, now)
		if err != nil {
			return err
		}
		if err := s.store.Grant(ctx, grant); err != nil {
			return err
		}
		return s.board.SeedDefaultLanes(ctx, project.UUID)
	})
	if err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "project created",
		"project", project.UUID, "slug", project.Slug, "by", user.UUID)
	return project, nil
}

// Rename edits a project's name and description. Manager or above.
func (s *Service) Rename(ctx context.Context, access *Access, name, description string) error {
	if !access.Subject.Can(authz.ActionProjectRename) {
		return apperrors.Forbidden("You cannot change this project's settings.")
	}
	if err := access.Project.Rename(name, description, s.now()); err != nil {
		return err
	}
	return s.store.Update(ctx, access.Project)
}

// Archive hides a project without losing it. Operator only.
func (s *Service) Archive(ctx context.Context, access *Access) error {
	if !access.Subject.Can(authz.ActionProjectArchive) {
		return apperrors.Forbidden("Only the owner can archive a project.")
	}
	access.Project.Archive(s.now())
	return s.store.Update(ctx, access.Project)
}

// Restore brings an archived project back. Operator only.
func (s *Service) Restore(ctx context.Context, access *Access) error {
	if !access.Subject.Can(authz.ActionProjectArchive) {
		return apperrors.Forbidden("Only the owner can restore a project.")
	}
	access.Project.Restore(s.now())
	return s.store.Update(ctx, access.Project)
}

// Delete removes a project and everything on it. Operator only.
//
// This genuinely erases the board — lanes, cards, labels, grants and activity all
// cascade. Archive is the reversible option, and is what the UI offers first.
func (s *Service) Delete(ctx context.Context, access *Access) error {
	if !access.Subject.Can(authz.ActionProjectDelete) {
		return apperrors.Forbidden("Only the owner can delete a project.")
	}
	if err := s.store.Delete(ctx, access.Project.UUID); err != nil {
		return err
	}
	s.log.WarnContext(ctx, "project deleted", "project", access.Project.UUID)
	return nil
}

// Member is a grant with the person's name attached.
type Member struct {
	Membership *domain.Membership
	User       *authdomain.User
}

// Members lists everyone granted a project, newest grant last.
func (s *Service) Members(ctx context.Context, access *Access) ([]Member, error) {
	grants, err := s.store.Members(ctx, access.Project.UUID)
	if err != nil {
		return nil, err
	}

	out := make([]Member, 0, len(grants))
	for _, g := range grants {
		user, err := s.users.UserByUUID(ctx, g.UserUUID)
		if err != nil {
			return nil, err
		}
		if user == nil {
			// The row cascades with the account, so this is unreachable. Skip
			// rather than render a member with no name.
			continue
		}
		out = append(out, Member{Membership: g, User: user})
	}
	return out, nil
}

// Grantable lists accounts that could be added to a project — everyone who can
// sign in and is not already a member.
func (s *Service) Grantable(ctx context.Context, access *Access) ([]*authdomain.User, error) {
	all, err := s.users.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	granted, err := s.store.Members(ctx, access.Project.UUID)
	if err != nil {
		return nil, err
	}

	already := make(map[string]struct{}, len(granted))
	for _, g := range granted {
		already[g.UserUUID] = struct{}{}
	}

	out := make([]*authdomain.User, 0, len(all))
	for _, u := range all {
		if _, member := already[u.UUID]; member {
			continue
		}
		// A suspended account cannot sign in, so offering it would be offering
		// access that does not work.
		if !u.CanSignIn() {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

// Grant adds or re-roles a member.
//
// Granting *manager* is the operator's alone: a manager who could mint peers could
// escalate the board out of the owner's control. A manager may add and remove
// ordinary members, which is the whole point of the role.
func (s *Service) Grant(ctx context.Context, access *Access, userUUID string, role domain.ProjectRole) error {
	action := authz.ActionMemberGrant
	if role == domain.RoleManager {
		action = authz.ActionMemberGrantManager
	}
	if !access.Subject.Can(action) {
		if role == domain.RoleManager {
			return apperrors.Forbidden("Only the owner can make somebody a manager.")
		}
		return apperrors.Forbidden("You cannot change who has access to this project.")
	}

	user, err := s.users.UserByUUID(ctx, userUUID)
	if err != nil {
		return err
	}
	if user == nil {
		return apperrors.NotFound("No such person.")
	}

	return s.tx.InTx(ctx, func(ctx context.Context) error {
		// Demoting the only manager would leave a board nobody but the operator
		// can configure. Checked inside the transaction, against the same state
		// the write applies to.
		if role != domain.RoleManager {
			existing, err := s.store.Membership(ctx, access.Project.UUID, userUUID)
			if err != nil {
				return err
			}
			if existing != nil && existing.Role == domain.RoleManager {
				if err := s.requireAnotherManager(ctx, access.Project.UUID); err != nil {
					return err
				}
			}
		}

		grant, err := domain.NewMembership(
			access.Project.UUID, userUUID, role, access.Subject.User.UUID, s.now())
		if err != nil {
			return err
		}
		return s.store.Grant(ctx, grant)
	})
}

// Revoke removes a member's access.
func (s *Service) Revoke(ctx context.Context, access *Access, userUUID string) error {
	if !access.Subject.Can(authz.ActionMemberGrant) {
		return apperrors.Forbidden("You cannot change who has access to this project.")
	}

	return s.tx.InTx(ctx, func(ctx context.Context) error {
		existing, err := s.store.Membership(ctx, access.Project.UUID, userUUID)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrNotAMember
		}

		// A manager may add and remove members; removing another *manager* is
		// the operator's call, for the same reason promoting one is.
		if existing.Role == domain.RoleManager {
			if !access.Subject.Can(authz.ActionMemberGrantManager) {
				return apperrors.Forbidden("Only the owner can remove a manager.")
			}
			if err := s.requireAnotherManager(ctx, access.Project.UUID); err != nil {
				return err
			}
		}

		return s.store.Revoke(ctx, access.Project.UUID, userUUID)
	})
}

// requireAnotherManager refuses a change that would take the last one away.
func (s *Service) requireAnotherManager(ctx context.Context, projectUUID string) error {
	managers, err := s.store.CountManagers(ctx, projectUUID)
	if err != nil {
		return err
	}
	if managers <= 1 {
		return ErrLastManager
	}
	return nil
}

// CanSee reports whether somebody may open a board.
//
// The card slice uses it to refuse an assignee with no grant: the foreign key would
// still catch an unknown uuid, but a known person with no access would otherwise be
// assigned work they cannot see. The operator can see every board, which is why this
// cannot be a bare membership lookup.
func (s *Service) CanSee(ctx context.Context, projectUUID, userUUID string) (bool, error) {
	user, err := s.users.UserByUUID(ctx, userUUID)
	if err != nil || user == nil {
		return false, err
	}

	membership, err := s.store.Membership(ctx, projectUUID, userUUID)
	if err != nil {
		return false, err
	}

	var role *domain.ProjectRole
	if membership != nil {
		role = &membership.Role
	}
	return authz.NewSubject(user, role).CanSeeProject(), nil
}

// Person is somebody who can see a board, reduced to what a picker needs.
type Person struct {
	UUID        string
	DisplayName string
	Email       string
}

// People returns everyone who may open a board, for an assignee picker.
//
// The operator is included even without a grant — their access is global, and a board
// they administer is one they can be assigned work on. Suspended accounts are left out:
// assigning work to somebody who cannot sign in is assigning it to nobody.
func (s *Service) People(ctx context.Context, projectUUID string) ([]Person, error) {
	grants, err := s.store.Members(ctx, projectUUID)
	if err != nil {
		return nil, err
	}

	granted := make(map[string]struct{}, len(grants))
	for _, g := range grants {
		granted[g.UserUUID] = struct{}{}
	}

	all, err := s.users.ListUsers(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Person, 0, len(grants)+1)
	for _, u := range all {
		if !u.CanSignIn() {
			continue
		}
		_, isMember := granted[u.UUID]
		if !isMember && !u.IsSuperAdmin() {
			continue
		}
		out = append(out, Person{UUID: u.UUID, DisplayName: u.DisplayName, Email: u.Email})
	}
	return out, nil
}
