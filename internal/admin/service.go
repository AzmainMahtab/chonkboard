// Package admin is the operator's surface: accounts, projects, and who has access to
// what.
//
// It has no `domain` and no `store`, deliberately. Admin owns no entities — every row
// it touches belongs to auth or to project — so a domain package here would hold nothing
// and a store would be a second way to reach tables that already have one. What it owns
// is the *composition*: the dashboard's read model, and the orchestration that spans both
// slices.
package admin

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/auth"
	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
)

// Accounts is the slice of the auth service this one needs.
//
// Declared here, by the consumer. Everything on it is already refused to a non-operator
// inside auth, so this package's own check is the second line rather than the only one.
type Accounts interface {
	ListUsers(ctx context.Context) ([]*authdomain.User, error)
	UserByUUID(ctx context.Context, uuid string) (*authdomain.User, error)
	CreateUser(ctx context.Context, actor *authdomain.User, in auth.CreateUserInput) (*auth.Credential, error)
	UpdateUser(ctx context.Context, actor *authdomain.User, userUUID string, in auth.UpdateUserInput) (*authdomain.User, error)
	ResetPassword(ctx context.Context, actor *authdomain.User, userUUID string) (*auth.Credential, error)
	SetSuspended(ctx context.Context, actor *authdomain.User, userUUID string, suspended bool) (*authdomain.User, error)
	RevokeAllSessions(ctx context.Context, actor *authdomain.User, userUUID string) (int64, error)
	SessionCount(ctx context.Context, userUUID string, at time.Time) (int, error)
}

// Service is the operator's read model and the orchestration across slices.
type Service struct {
	accounts Accounts
	projects ProjectDirectory
	log      *slog.Logger
	now      func() time.Time
}

// ProjectDirectory is what the dashboard needs about projects: every one of them, and
// who has access to each.
//
// Its row types are the project slice's own. Mirroring them here would give two
// structurally identical types that Go will not convert across a slice, so the wiring
// would need an adapter loop for no gain — and a second definition to keep in step.
type ProjectDirectory interface {
	ListAllForAdmin(ctx context.Context, actor *authdomain.User, includeArchived bool) ([]project.AdminProjectRow, error)
	MembersForAdmin(ctx context.Context, actor *authdomain.User, projectUUID string) ([]project.AdminMemberRow, error)
}

// ProjectRow and MemberRow name the project slice's shapes locally, so this package's
// own signatures read without a qualifier on every line.
type (
	ProjectRow = project.AdminProjectRow
	MemberRow  = project.AdminMemberRow
)

// NewService wires the service.
func NewService(accounts Accounts, projects ProjectDirectory, log *slog.Logger) *Service {
	return &Service{accounts: accounts, projects: projects, log: log, now: time.Now}
}

// WithClock replaces the clock. Tests only.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// AccountRow is one account as the console lists it.
type AccountRow struct {
	User *authdomain.User
	// Sessions is how many live sessions they hold, so the console can show whether
	// somebody is signed in right now.
	Sessions int
	// Projects is how many boards they have been granted.
	Projects int
	// IsSelf marks the signed-in operator, who is not offered controls that would
	// lock them out.
	IsSelf bool
}

// Overview is the dashboard.
type Overview struct {
	Accounts []AccountRow
	Projects []ProjectRow
	// Access is who can reach what, keyed by project uuid. The answer to "who has
	// access to what", which is the question the console exists to answer.
	Access map[string][]MemberRow
	// Operators counts the super-admin accounts that can still sign in, so the
	// console can warn when there is only one.
	Operators int
}

// Dashboard builds the operator's view.
//
// Refused to anybody else. The underlying services refuse too, but returning early here
// means a member never causes the queries to run at all.
func (s *Service) Dashboard(
	ctx context.Context, actor *authdomain.User, includeArchived bool,
) (*Overview, error) {
	if err := s.mustBeOperator(actor); err != nil {
		return nil, err
	}

	users, err := s.accounts.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.projects.ListAllForAdmin(ctx, actor, includeArchived)
	if err != nil {
		return nil, err
	}

	overview := &Overview{
		Projects: projects,
		Access:   make(map[string][]MemberRow, len(projects)),
	}

	// Grants per project, and a count per person, from one pass.
	grantsPerUser := make(map[string]int, len(users))
	for _, p := range projects {
		members, err := s.projects.MembersForAdmin(ctx, actor, p.UUID)
		if err != nil {
			return nil, err
		}
		overview.Access[p.UUID] = members
		for _, m := range members {
			grantsPerUser[m.UserUUID]++
		}
	}

	now := s.now()
	for _, u := range users {
		sessions, err := s.accounts.SessionCount(ctx, u.UUID, now)
		if err != nil {
			return nil, err
		}
		if u.IsSuperAdmin() && u.CanSignIn() {
			overview.Operators++
		}
		overview.Accounts = append(overview.Accounts, AccountRow{
			User:     u,
			Sessions: sessions,
			Projects: grantsPerUser[u.UUID],
			IsSelf:   u.UUID == actor.UUID,
		})
	}

	// Suspended accounts last, then by name: the list is read to find somebody, and a
	// suspended account is rarely who is being looked for.
	sort.SliceStable(overview.Accounts, func(i, j int) bool {
		a, b := overview.Accounts[i].User, overview.Accounts[j].User
		if a.CanSignIn() != b.CanSignIn() {
			return a.CanSignIn()
		}
		return a.DisplayName < b.DisplayName
	})

	return overview, nil
}

// CreateAccount makes an account and returns its one-time password.
func (s *Service) CreateAccount(
	ctx context.Context, actor *authdomain.User, in auth.CreateUserInput,
) (*auth.Credential, error) {
	if err := s.mustBeOperator(actor); err != nil {
		return nil, err
	}
	return s.accounts.CreateUser(ctx, actor, in)
}

// EditAccount changes somebody's name, address or global role.
func (s *Service) EditAccount(
	ctx context.Context, actor *authdomain.User, userUUID string, in auth.UpdateUserInput,
) (*authdomain.User, error) {
	if err := s.mustBeOperator(actor); err != nil {
		return nil, err
	}
	return s.accounts.UpdateUser(ctx, actor, userUUID, in)
}

// ResetPassword generates a new one-time password and ends every session that account
// holds.
func (s *Service) ResetPassword(
	ctx context.Context, actor *authdomain.User, userUUID string,
) (*auth.Credential, error) {
	if err := s.mustBeOperator(actor); err != nil {
		return nil, err
	}
	return s.accounts.ResetPassword(ctx, actor, userUUID)
}

// SetSuspended suspends or reinstates an account.
func (s *Service) SetSuspended(
	ctx context.Context, actor *authdomain.User, userUUID string, suspended bool,
) (*authdomain.User, error) {
	if err := s.mustBeOperator(actor); err != nil {
		return nil, err
	}
	return s.accounts.SetSuspended(ctx, actor, userUUID, suspended)
}

// SignOutEverywhere ends every session an account holds, without changing its password.
func (s *Service) SignOutEverywhere(
	ctx context.Context, actor *authdomain.User, userUUID string,
) (int64, error) {
	if err := s.mustBeOperator(actor); err != nil {
		return 0, err
	}
	return s.accounts.RevokeAllSessions(ctx, actor, userUUID)
}

// Account returns one account, for the edit form.
func (s *Service) Account(
	ctx context.Context, actor *authdomain.User, userUUID string,
) (*authdomain.User, error) {
	if err := s.mustBeOperator(actor); err != nil {
		return nil, err
	}
	user, err := s.accounts.UserByUUID(ctx, userUUID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, auth.ErrNoSuchUser
	}
	return user, nil
}

// mustBeOperator is the one check every method here starts with.
//
// A 404 rather than a 403: the console is the operator's surface, and a member should
// not learn it exists. Same reasoning as the debug route.
func (s *Service) mustBeOperator(actor *authdomain.User) error {
	if authz.NewSubject(actor, nil).Can(authz.ActionUserListAll) {
		return nil
	}
	return apperrors.NotFound("Not found.")
}
