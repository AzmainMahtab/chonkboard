// Package domain holds the rules about projects and who was granted one.
//
// A grant row is the whole of "this person can see this project"; absence is
// denial, so there is no negative grant and no inherited access.
package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// ProjectRole is a role held inside one project. It is unrelated to the global
// role in the auth slice: a global member can be a manager of one project and an
// ordinary member of another.
type ProjectRole string

const (
	// RoleManager may change the shape of the board: lanes, labels, and who
	// else is a member.
	RoleManager ProjectRole = "manager"
	// RoleMember may work on cards and nothing else.
	RoleMember ProjectRole = "member"
)

// Valid reports whether r is one of the roles the schema's CHECK allows.
func (r ProjectRole) Valid() bool { return r == RoleManager || r == RoleMember }

// CanManageBoard reports whether this role may alter lanes, labels, or grants.
// It is the requirement the whole permission model exists for: a member drags,
// creates, and edits cards, and can do nothing to the board itself.
func (r ProjectRole) CanManageBoard() bool { return r == RoleManager }

// Field bounds. The schema checks the slug shape and that names are non-empty;
// the upper bounds are domain rules.
const (
	MaxSlugLength        = 40
	MaxNameLength        = 120
	MaxDescriptionLength = 2000
)

// slugPattern is the shape a URL-safe project key must take. It is intentionally
// narrower than the schema's CHECK, which only enforces lower-case and length:
// the database guards the invariant, and this guards the user experience.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Project is a board.
type Project struct {
	UUID        string
	Slug        string
	Name        string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

// NormaliseSlug folds a slug to the stored form. The schema's CHECK requires
// lower case, so anything entering the system passes through here or the insert
// fails.
func NormaliseSlug(slug string) string {
	return strings.ToLower(strings.TrimSpace(slug))
}

// SlugFromName derives a URL key from a display name, which is what the create
// form offers as a default.
func SlugFromName(name string) string {
	var b strings.Builder
	lastHyphen := true // leading hyphens are suppressed
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > MaxSlugLength {
		slug = strings.Trim(slug[:MaxSlugLength], "-")
	}
	return slug
}

// NewProject validates and builds a board.
func NewProject(uuid, slug, name, description, createdBy string, now time.Time) (*Project, error) {
	slug = NormaliseSlug(slug)
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)

	err := apperrors.Validation("that project is not valid")
	invalid := false

	if uuid == "" {
		err = err.WithField("uuid", "is required")
		invalid = true
	}
	if problem := validateSlug(slug); problem != "" {
		err = err.WithField("slug", problem)
		invalid = true
	}
	if problem := validateName(name); problem != "" {
		err = err.WithField("name", problem)
		invalid = true
	}
	if utf8.RuneCountInString(description) > MaxDescriptionLength {
		err = err.WithField("description", "is too long")
		invalid = true
	}
	if createdBy == "" {
		err = err.WithField("created_by", "is required")
		invalid = true
	}
	if invalid {
		return nil, err
	}

	return &Project{
		UUID:        uuid,
		Slug:        slug,
		Name:        name,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
	}, nil
}

func validateSlug(slug string) string {
	switch {
	case slug == "":
		return "is required"
	case len(slug) > MaxSlugLength:
		return "is too long"
	case !slugPattern.MatchString(slug):
		return "may use only lower-case letters, digits and single hyphens"
	}
	return ""
}

func validateName(name string) string {
	switch {
	case name == "":
		return "is required"
	case utf8.RuneCountInString(name) > MaxNameLength:
		return "is too long"
	}
	return ""
}

// Rename changes the display name and description.
func (p *Project) Rename(name, description string, now time.Time) error {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)

	err := apperrors.Validation("that project is not valid")
	invalid := false
	if problem := validateName(name); problem != "" {
		err = err.WithField("name", problem)
		invalid = true
	}
	if utf8.RuneCountInString(description) > MaxDescriptionLength {
		err = err.WithField("description", "is too long")
		invalid = true
	}
	if invalid {
		return err
	}

	p.Name = name
	p.Description = description
	p.UpdatedAt = now.UTC()
	return nil
}

// Archive hides the project without deleting anything.
func (p *Project) Archive(now time.Time) {
	if p.ArchivedAt == nil {
		at := now.UTC()
		p.ArchivedAt = &at
		p.UpdatedAt = at
	}
}

// Restore brings an archived project back.
func (p *Project) Restore(now time.Time) {
	p.ArchivedAt = nil
	p.UpdatedAt = now.UTC()
}

// IsArchived reports whether the project is hidden.
func (p *Project) IsArchived() bool { return p.ArchivedAt != nil }

// Membership is a grant of access to one project.
type Membership struct {
	ProjectUUID string
	UserUUID    string
	Role        ProjectRole
	GrantedBy   string
	GrantedAt   time.Time
}

// NewMembership validates and builds a grant.
func NewMembership(projectUUID, userUUID string, role ProjectRole, grantedBy string, now time.Time) (*Membership, error) {
	err := apperrors.Validation("that grant is not valid")
	invalid := false

	if projectUUID == "" {
		err = err.WithField("project_uuid", "is required")
		invalid = true
	}
	if userUUID == "" {
		err = err.WithField("user_uuid", "is required")
		invalid = true
	}
	if !role.Valid() {
		err = err.WithField("role", "is not a known role")
		invalid = true
	}
	if grantedBy == "" {
		err = err.WithField("granted_by", "is required")
		invalid = true
	}
	if invalid {
		return nil, err
	}

	return &Membership{
		ProjectUUID: projectUUID,
		UserUUID:    userUUID,
		Role:        role,
		GrantedBy:   grantedBy,
		GrantedAt:   now.UTC(),
	}, nil
}
