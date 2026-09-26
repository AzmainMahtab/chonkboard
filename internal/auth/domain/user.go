// Package domain holds the identity rules: who a user is, what state their
// account is in, and when a session still counts. It imports no framework and
// touches no database, so every rule here is testable on its own.
package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Role is a user's global role. Project-level roles are separate and live in the
// project slice; this one only distinguishes the operator from everyone else.
type Role string

const (
	// RoleSuperAdmin is the operator. There is normally exactly one.
	RoleSuperAdmin Role = "super_admin"
	// RoleMember is everyone else. Their reach is whatever project grants say.
	RoleMember Role = "member"
)

// Valid reports whether r is one of the roles the schema's CHECK allows.
func (r Role) Valid() bool { return r == RoleSuperAdmin || r == RoleMember }

// Status is whether an account may sign in.
type Status string

const (
	// StatusActive can sign in.
	StatusActive Status = "active"
	// StatusSuspended cannot, and has every session revoked when set.
	StatusSuspended Status = "suspended"
)

// Valid reports whether s is one of the statuses the schema's CHECK allows.
func (s Status) Valid() bool { return s == StatusActive || s == StatusSuspended }

// Limits on the free-text fields. The schema only checks these are non-empty;
// the upper bounds are a domain rule because a 10KB display name is a UI problem
// rather than a data-integrity one.
const (
	MaxEmailLength       = 254 // RFC 5321
	MaxDisplayNameLength = 80
)

// User is an account.
type User struct {
	UUID               string
	Email              string
	DisplayName        string
	PasswordHash       string
	Role               Role
	Status             Status
	MustChangePassword bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NormaliseEmail lower-cases and trims an address.
//
// This is not cosmetic. Case-insensitive uniqueness has no portable DDL —
// SQLite's COLLATE NOCASE and PostgreSQL's CITEXT are both dialect-specific — so
// the schema stores addresses already folded and a CHECK enforces it. Every
// address entering the system goes through here, or the insert fails.
func NormaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NewUser validates and builds an account. The caller supplies the id and the
// already-hashed password: hashing is a service concern, and generating the id
// here would make the function untestable without a clock.
func NewUser(
	uuid, email, displayName, passwordHash string,
	role Role,
	now time.Time,
) (*User, error) {
	email = NormaliseEmail(email)
	displayName = strings.TrimSpace(displayName)

	err := apperrors.Validation("that account is not valid")
	invalid := false

	if uuid == "" {
		err = err.WithField("uuid", "is required")
		invalid = true
	}
	if problem := validateEmail(email); problem != "" {
		err = err.WithField("email", problem)
		invalid = true
	}
	if problem := validateDisplayName(displayName); problem != "" {
		err = err.WithField("display_name", problem)
		invalid = true
	}
	if passwordHash == "" {
		err = err.WithField("password", "is required")
		invalid = true
	}
	if !role.Valid() {
		err = err.WithField("role", "is not a known role")
		invalid = true
	}
	if invalid {
		return nil, err
	}

	return &User{
		UUID:         uuid,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		Role:         role,
		Status:       StatusActive,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}, nil
}

func validateEmail(email string) string {
	if email == "" {
		return "is required"
	}
	if len(email) > MaxEmailLength {
		return "is too long"
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "is not a valid address"
	}
	return ""
}

func validateDisplayName(name string) string {
	if name == "" {
		return "is required"
	}
	if utf8.RuneCountInString(name) > MaxDisplayNameLength {
		return "is too long"
	}
	return ""
}

// Rename changes the display name.
func (u *User) Rename(displayName string, now time.Time) error {
	displayName = strings.TrimSpace(displayName)
	if problem := validateDisplayName(displayName); problem != "" {
		return apperrors.Validation("that name is not valid").
			WithField("display_name", problem)
	}
	u.DisplayName = displayName
	u.UpdatedAt = now.UTC()
	return nil
}

// ChangeEmail replaces the address. Uniqueness is the store's to enforce; this
// only checks the shape.
func (u *User) ChangeEmail(email string, now time.Time) error {
	email = NormaliseEmail(email)
	if problem := validateEmail(email); problem != "" {
		return apperrors.Validation("that address is not valid").
			WithField("email", problem)
	}
	u.Email = email
	u.UpdatedAt = now.UTC()
	return nil
}

// SetPassword installs a new hash.
//
// forceChange is what the operator uses when handing over a credential they
// chose: the user is made to replace it on next sign-in. A user changing their
// own password passes false.
func (u *User) SetPassword(hash string, forceChange bool, now time.Time) error {
	if hash == "" {
		return apperrors.Validation("that password is not valid").
			WithField("password", "is required")
	}
	u.PasswordHash = hash
	u.MustChangePassword = forceChange
	u.UpdatedAt = now.UTC()
	return nil
}

// Suspend blocks sign-in. The caller is responsible for revoking live sessions;
// without that the account keeps working until its cookie expires.
func (u *User) Suspend(now time.Time) {
	u.Status = StatusSuspended
	u.UpdatedAt = now.UTC()
}

// Reinstate allows sign-in again.
func (u *User) Reinstate(now time.Time) {
	u.Status = StatusActive
	u.UpdatedAt = now.UTC()
}

// Promote makes the account a super admin.
func (u *User) Promote(now time.Time) {
	u.Role = RoleSuperAdmin
	u.UpdatedAt = now.UTC()
}

// Demote returns the account to an ordinary member.
func (u *User) Demote(now time.Time) {
	u.Role = RoleMember
	u.UpdatedAt = now.UTC()
}

// IsSuperAdmin reports whether this account is the operator.
func (u *User) IsSuperAdmin() bool { return u.Role == RoleSuperAdmin }

// CanSignIn reports whether this account is permitted to start a session.
func (u *User) CanSignIn() bool { return u.Status == StatusActive }
