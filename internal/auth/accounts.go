package auth

import (
	"context"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// The operator's account operations. Everything here is refused to anybody else, and
// every one of them is checked against authz rather than against a role field, so the
// matrix stays the single place a permission question resolves.

// The failures these report.
var (
	// ErrNoSuchUser covers an account that does not exist.
	ErrNoSuchUser = apperrors.NotFound("No such account.")

	// ErrEmailTaken is a duplicate address, reported on the field.
	ErrEmailTaken = apperrors.Conflict("An account already uses that email.").
			WithField("email", "is already taken")

	// ErrLastOperator refuses a change that would leave the installation with
	// nobody able to administer it.
	ErrLastOperator = apperrors.Conflict(
		"That is the only owner account. Promote somebody else first.")

	// ErrNotYourself refuses an operator suspending their own account, which would
	// lock them out mid-request with no way back in.
	ErrNotYourself = apperrors.Conflict("You cannot suspend your own account.")
)

// Credential is a password handed back to be shown exactly once.
//
// It exists as its own type so that a plaintext password is never mistaken for a hash
// in a signature, and so the one place it is allowed to travel is obvious in a diff.
// It is never stored, never logged, and never returned a second time.
type Credential struct {
	User *domain.User
	// Password is the generated plaintext. The only copy that will ever exist.
	Password string
}

// CreateUserInput is a new account.
type CreateUserInput struct {
	Email       string
	DisplayName string
	// MakeOperator grants the super-admin role. Rare, and deliberate.
	MakeOperator bool
}

// CreateUser makes an account and returns its one-time password.
//
// The password is generated rather than chosen by the operator: a password somebody
// else picked for you is one they know, and one that tends to be reused across the
// accounts they create. `must_change_password` is set, so it cannot survive first use.
//
// There is no email in this system, so the credential is handed over in person or over
// a channel the operator trusts. That is why it is returned here and shown once, and why
// losing it means resetting it.
func (s *Service) CreateUser(
	ctx context.Context, actor *domain.User, in CreateUserInput,
) (*Credential, error) {
	if !authz.NewSubject(actor, nil).Can(authz.ActionUserCreate) {
		return nil, apperrors.Forbidden("Only the owner can create an account.")
	}

	plaintext, err := domain.GeneratePassword()
	if err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(plaintext)
	if err != nil {
		return nil, apperrors.Internal(err, "hash password")
	}

	role := domain.RoleMember
	if in.MakeOperator {
		role = domain.RoleSuperAdmin
	}

	user, err := domain.NewUser(
		idgenerator.NewUUIDv7(), in.Email, in.DisplayName, hash, role, s.now())
	if err != nil {
		return nil, err
	}
	user.MustChangePassword = true

	if err := s.store.CreateUser(ctx, user); err != nil {
		if apperrors.From(err).Code == apperrors.CodeConflict {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	// The uuid and the role, never the password.
	s.log.InfoContext(ctx, "account created",
		"user", user.UUID, "role", user.Role, "by", actor.UUID)

	return &Credential{User: user, Password: plaintext}, nil
}

// ResetPassword generates a new password for somebody else and ends every session they
// hold.
//
// The revocation is the point as much as the new password is: a reset is what an
// operator does when a credential may be compromised, and a session that outlived it
// would survive the very thing meant to end it.
func (s *Service) ResetPassword(
	ctx context.Context, actor *domain.User, userUUID string,
) (*Credential, error) {
	if !authz.NewSubject(actor, nil).Can(authz.ActionUserResetPassword) {
		return nil, apperrors.Forbidden("Only the owner can reset a password.")
	}

	user, err := s.store.UserByUUID(ctx, userUUID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNoSuchUser
	}

	plaintext, err := domain.GeneratePassword()
	if err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(plaintext)
	if err != nil {
		return nil, apperrors.Internal(err, "hash password")
	}

	now := s.now()
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		// forceChange: a password the operator generated must not stay in use.
		if err := user.SetPassword(hash, true, now); err != nil {
			return err
		}
		if err := s.store.UpdateUser(ctx, user); err != nil {
			return err
		}
		revoked, err := s.store.RevokeSessionsForUser(ctx, user.UUID, now)
		if err != nil {
			return err
		}
		s.log.InfoContext(ctx, "password reset",
			"user", user.UUID, "sessions_revoked", revoked, "by", actor.UUID)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &Credential{User: user, Password: plaintext}, nil
}

// SetSuspended suspends or reinstates an account.
//
// Suspending revokes every session immediately, so somebody working in the application
// is locked out on their next request rather than when their session happens to expire.
// The session middleware refuses a suspended account on every request, which is what
// makes that immediate.
func (s *Service) SetSuspended(
	ctx context.Context, actor *domain.User, userUUID string, suspended bool,
) (*domain.User, error) {
	if !authz.NewSubject(actor, nil).Can(authz.ActionUserSuspend) {
		return nil, apperrors.Forbidden("Only the owner can suspend an account.")
	}

	user, err := s.store.UserByUUID(ctx, userUUID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNoSuchUser
	}

	// An operator suspending themselves would be locked out on the next request with
	// no way back in but editing the database by hand.
	if suspended && user.UUID == actor.UUID {
		return nil, ErrNotYourself
	}

	now := s.now()
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if suspended {
			// The last operator must stay able to sign in, or the installation has
			// nobody who can administer it.
			if user.IsSuperAdmin() {
				if err := s.requireAnotherOperator(ctx, user.UUID); err != nil {
					return err
				}
			}
			user.Suspend(now)
		} else {
			user.Reinstate(now)
		}

		if err := s.store.UpdateUser(ctx, user); err != nil {
			return err
		}
		if !suspended {
			return nil
		}

		revoked, err := s.store.RevokeSessionsForUser(ctx, user.UUID, now)
		if err != nil {
			return err
		}
		s.log.InfoContext(ctx, "account suspended",
			"user", user.UUID, "sessions_revoked", revoked, "by", actor.UUID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// UpdateUserInput is the editable part of an account.
type UpdateUserInput struct {
	DisplayName string
	Email       string
	// IsOperator is the global role. Changing it is how an operator hands over, or
	// takes back, administration of the installation.
	IsOperator bool
}

// UpdateUser edits somebody's name, address and global role.
func (s *Service) UpdateUser(
	ctx context.Context, actor *domain.User, userUUID string, in UpdateUserInput,
) (*domain.User, error) {
	if !authz.NewSubject(actor, nil).Can(authz.ActionUserCreate) {
		return nil, apperrors.Forbidden("Only the owner can edit an account.")
	}

	user, err := s.store.UserByUUID(ctx, userUUID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNoSuchUser
	}

	now := s.now()
	if err := user.Rename(in.DisplayName, now); err != nil {
		return nil, err
	}
	if err := user.ChangeEmail(in.Email, now); err != nil {
		return nil, err
	}

	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		switch {
		case in.IsOperator && !user.IsSuperAdmin():
			user.Promote(now)
		case !in.IsOperator && user.IsSuperAdmin():
			// Demoting the last operator leaves nobody able to administer.
			if err := s.requireAnotherOperator(ctx, user.UUID); err != nil {
				return err
			}
			user.Demote(now)
		}

		if err := s.store.UpdateUser(ctx, user); err != nil {
			if apperrors.From(err).Code == apperrors.CodeConflict {
				return ErrEmailTaken
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// requireAnotherOperator refuses a change that would take the last one away.
func (s *Service) requireAnotherOperator(ctx context.Context, excluding string) error {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.UUID != excluding && u.IsSuperAdmin() && u.CanSignIn() {
			return nil
		}
	}
	return ErrLastOperator
}

// SessionCount reports how many live sessions an account holds, so the console can show
// whether somebody is currently signed in.
func (s *Service) SessionCount(ctx context.Context, userUUID string, at time.Time) (int, error) {
	return s.store.CountLiveSessionsForUser(ctx, userUUID, at)
}

// RevokeAllSessions signs somebody out everywhere without changing their password.
func (s *Service) RevokeAllSessions(
	ctx context.Context, actor *domain.User, userUUID string,
) (int64, error) {
	if !authz.NewSubject(actor, nil).Can(authz.ActionUserSuspend) {
		return 0, apperrors.Forbidden("Only the owner can sign somebody out.")
	}
	return s.store.RevokeSessionsForUser(ctx, userUUID, s.now())
}
