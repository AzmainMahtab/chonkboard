package auth

import (
	"context"

	"github.com/AzmainMahtab/chonkboard/internal/auth/domain"
)

// The read side of the account list, exposed on the service so other slices reach
// accounts through this slice's exported interface rather than its store.
//
// A member picker and an assignee dropdown both need names for uuids, and that is
// auth's data. These are the only two shapes anything outside this package needs.

// UserByUUID returns one account, or (nil, nil) when there is none.
func (s *Service) UserByUUID(ctx context.Context, uuid string) (*domain.User, error) {
	return s.store.UserByUUID(ctx, uuid)
}

// ListUsers returns every account, ordered by creation.
//
// Suspended accounts are included: a suspended person may still hold a project
// grant and must still be visible in the member list, or the operator cannot see
// who to revoke. Callers that are offering somebody to *add* filter on CanSignIn.
func (s *Service) ListUsers(ctx context.Context) ([]*domain.User, error) {
	return s.store.ListUsers(ctx)
}

// DisplayName returns a person's name for rendering, or a placeholder when the
// account has gone.
//
// A card outlives the person who made it — `created_by` is ON DELETE RESTRICT, so it
// cannot vanish, but an assignee is ON DELETE SET NULL and activity rows can outlive
// their actor in other shapes. Returning a placeholder rather than an error keeps one
// missing name from failing a whole page render.
func (s *Service) DisplayName(ctx context.Context, userUUID string) string {
	if userUUID == "" {
		return "Somebody"
	}
	user, err := s.store.UserByUUID(ctx, userUUID)
	if err != nil {
		s.log.WarnContext(ctx, "cannot resolve display name", "user", userUUID, "error", err)
		return "Somebody"
	}
	if user == nil {
		return "A removed account"
	}
	return user.DisplayName
}
