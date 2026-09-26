package project

import (
	"context"
	"time"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
)

// The operator's read model over projects, for the admin console.
//
// Separate from the handler's own view-building because the console asks a different
// question: not "what is on this board" but "who can reach what", across every board at
// once. The shapes it returns are the admin slice's, which this package satisfies rather
// than defines — admin declares the port, as the consumer.

// AdminProjectRow mirrors admin.ProjectRow. Declared here rather than imported so this
// package does not depend on admin; the two are matched by shape at the wiring point.
type AdminProjectRow struct {
	UUID       string
	Slug       string
	Name       string
	IsArchived bool
	Lanes      int
	Cards      int
	Members    int
	UpdatedAt  time.Time
}

// AdminMemberRow mirrors admin.MemberRow.
type AdminMemberRow struct {
	UserUUID    string
	DisplayName string
	Email       string
	Role        domain.ProjectRole
	Suspended   bool
}

// ListAllForAdmin returns every project with its counts. Operator only.
func (s *Service) ListAllForAdmin(
	ctx context.Context, actor *authdomain.User, includeArchived bool,
) ([]AdminProjectRow, error) {
	if !authz.NewSubject(actor, nil).Can(authz.ActionProjectListAll) {
		return nil, apperrors.NotFound("Not found.")
	}

	projects, err := s.store.ListAll(ctx, includeArchived)
	if err != nil {
		return nil, err
	}

	out := make([]AdminProjectRow, 0, len(projects))
	for _, p := range projects {
		members, err := s.store.Members(ctx, p.UUID)
		if err != nil {
			return nil, err
		}
		out = append(out, AdminProjectRow{
			UUID:       p.UUID,
			Slug:       p.Slug,
			Name:       p.Name,
			IsArchived: p.IsArchived(),
			Members:    len(members),
			UpdatedAt:  p.UpdatedAt,
		})
	}
	return out, nil
}

// MembersForAdmin returns who has access to one project. Operator only.
func (s *Service) MembersForAdmin(
	ctx context.Context, actor *authdomain.User, projectUUID string,
) ([]AdminMemberRow, error) {
	if !authz.NewSubject(actor, nil).Can(authz.ActionProjectListAll) {
		return nil, apperrors.NotFound("Not found.")
	}

	grants, err := s.store.Members(ctx, projectUUID)
	if err != nil {
		return nil, err
	}

	out := make([]AdminMemberRow, 0, len(grants))
	for _, g := range grants {
		user, err := s.users.UserByUUID(ctx, g.UserUUID)
		if err != nil {
			return nil, err
		}
		if user == nil {
			continue
		}
		out = append(out, AdminMemberRow{
			UserUUID:    user.UUID,
			DisplayName: user.DisplayName,
			Email:       user.Email,
			Role:        g.Role,
			Suspended:   !user.CanSignIn(),
		})
	}
	return out, nil
}
