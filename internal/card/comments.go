package card

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// Comments live in this file rather than in store.go and service.go because they are
// one cohesive feature: the model, its queries and its rules read together, and
// splitting them across the two general files makes both harder to follow.

// MaxCommentLength bounds a comment. Long enough for a real discussion, short enough
// that a card page does not become unbounded.
const MaxCommentLength = 5000

// The failures comments report.
var (
	// ErrNoSuchComment covers a comment that does not exist and one on another
	// board, for the same reason every other lookup does.
	ErrNoSuchComment = apperrors.NotFound("No such comment.")

	// ErrNotYourComment is what a member gets for somebody else's comment.
	ErrNotYourComment = apperrors.Forbidden("You can only delete your own comments.")
)

// Comment is one entry in a card's discussion.
type Comment struct {
	UUID       string
	CardUUID   string
	AuthorUUID string
	Body       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// DeletedAt marks a tombstone. A removed comment leaves one rather than
	// vanishing, so a thread does not silently lose its shape and a reply above it
	// stops making sense.
	DeletedAt *time.Time
}

// IsDeleted reports whether this is a tombstone.
func (c Comment) IsDeleted() bool { return c.DeletedAt != nil }

const commentColumns = `uuid, card_uuid, author_uuid, body, created_at, updated_at, deleted_at`

type commentModel struct {
	UUID       string            `db:"uuid"`
	CardUUID   string            `db:"card_uuid"`
	AuthorUUID string            `db:"author_uuid"`
	Body       string            `db:"body"`
	CreatedAt  database.Time     `db:"created_at"`
	UpdatedAt  database.Time     `db:"updated_at"`
	DeletedAt  database.NullTime `db:"deleted_at"`
}

func (m commentModel) toDomain() Comment {
	return Comment{
		UUID:       m.UUID,
		CardUUID:   m.CardUUID,
		AuthorUUID: m.AuthorUUID,
		Body:       m.Body,
		CreatedAt:  m.CreatedAt.Time(),
		UpdatedAt:  m.UpdatedAt.Time(),
		DeletedAt:  m.DeletedAt.Ptr(),
	}
}

// CreateComment inserts a comment.
func (s *Store) CreateComment(ctx context.Context, c Comment) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO comments (`+commentColumns+`)
		VALUES (:uuid, :card_uuid, :author_uuid, :body, :created_at, :updated_at, :deleted_at)`,
		commentModel{
			UUID: c.UUID, CardUUID: c.CardUUID, AuthorUUID: c.AuthorUUID, Body: c.Body,
			CreatedAt: database.NewTime(c.CreatedAt), UpdatedAt: database.NewTime(c.UpdatedAt),
			DeletedAt: database.NullTimeFrom(c.DeletedAt),
		})
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid("That card or person no longer exists.").Wrap(err)
		}
		return database.MapError(err, "create comment")
	}
	return nil
}

// CommentByUUID returns one comment, or (nil, nil) when there is none.
func (s *Store) CommentByUUID(ctx context.Context, uuid string) (*Comment, error) {
	var m commentModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m,
		`SELECT `+commentColumns+` FROM comments WHERE uuid = :uuid`,
		map[string]any{"uuid": uuid})
	if err != nil || !found {
		return nil, err
	}
	c := m.toDomain()
	return &c, nil
}

// CommentsForCard returns a card's discussion, oldest first — the order a thread is
// read in.
func (s *Store) CommentsForCard(ctx context.Context, cardUUID string) ([]Comment, error) {
	var models []commentModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT `+commentColumns+` FROM comments
		WHERE card_uuid = :card_uuid
		ORDER BY created_at, uuid`,
		map[string]any{"card_uuid": cardUUID}); err != nil {
		return nil, err
	}

	out := make([]Comment, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out, nil
}

// CountCommentsForCards returns how many live comments each card has.
//
// One query for a whole board rather than one per card: the count appears on every
// card in every lane.
func (s *Store) CountCommentsForCards(ctx context.Context, cardUUIDs []string) (map[string]int, error) {
	if len(cardUUIDs) == 0 {
		return map[string]int{}, nil
	}

	type row struct {
		CardUUID string `db:"card_uuid"`
		N        int    `db:"n"`
	}
	var rows []row
	if err := database.SelectIn(ctx, s.tx.Reader(ctx), &rows, `
		SELECT card_uuid, count(*) AS n FROM comments
		WHERE card_uuid IN (?) AND deleted_at IS NULL
		GROUP BY card_uuid`,
		cardUUIDs); err != nil {
		return nil, err
	}

	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.CardUUID] = r.N
	}
	return out, nil
}

// SoftDeleteComment marks a comment removed without dropping the row.
func (s *Store) SoftDeleteComment(ctx context.Context, uuid string, at time.Time) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE comments SET deleted_at = :at, updated_at = :at
		WHERE uuid = :uuid AND deleted_at IS NULL`,
		map[string]any{"at": database.NewTime(at), "uuid": uuid})
	if err != nil {
		return database.MapError(err, "delete comment")
	}
	// Already deleted is not an error: a double-submitted form must not show a
	// failure, and the outcome the person wanted has happened either way.
	_ = n
	return nil
}

// ValidateCommentBody checks a comment before it is written.
func ValidateCommentBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return "", apperrors.Validation("A comment needs something in it.").
			WithField("body", "is required")
	case utf8.RuneCountInString(body) > MaxCommentLength:
		return "", apperrors.Validation("That comment is too long.").
			WithField("body", "must be shorter than 5000 characters")
	}
	return body, nil
}

// Comment adds a comment to a card. Any granted member may comment.
func (s *Service) Comment(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID, body string,
) (*Comment, error) {
	if !subj.Can(authz.ActionCommentCreate) {
		return nil, apperrors.Forbidden("You cannot comment on this board.")
	}

	card, err := s.ByUUID(ctx, subj, projectUUID, cardUUID)
	if err != nil {
		return nil, err
	}

	body, err = ValidateCommentBody(body)
	if err != nil {
		return nil, err
	}

	now := s.now()
	comment := Comment{
		UUID:       idgenerator.NewUUIDv7(),
		CardUUID:   card.UUID,
		AuthorUUID: subj.User.UUID,
		Body:       body,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.CreateComment(ctx, comment); err != nil {
			return err
		}
		return s.record(ctx, card, ActivityCommented, nil, nil, subj.User.UUID)
	})
	if err != nil {
		return nil, err
	}
	return &comment, nil
}

// Comments returns a card's discussion.
func (s *Service) Comments(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string,
) ([]Comment, error) {
	if _, err := s.ByUUID(ctx, subj, projectUUID, cardUUID); err != nil {
		return nil, err
	}
	return s.store.CommentsForCard(ctx, cardUUID)
}

// DeleteComment soft-deletes a comment.
//
// The author may remove their own; a manager may remove any on their board. Same
// ownership rule as a card, and the same reason: a shared board needs somebody able
// to clear up, without letting everybody rewrite everybody else's words.
func (s *Service) DeleteComment(
	ctx context.Context, subj authz.Subject, projectUUID, commentUUID string,
) (*Comment, error) {
	comment, err := s.store.CommentByUUID(ctx, commentUUID)
	if err != nil {
		return nil, err
	}
	if comment == nil {
		return nil, ErrNoSuchComment
	}

	// Scoped through the card, so a comment on another board is invisible.
	card, err := s.ByUUID(ctx, subj, projectUUID, comment.CardUUID)
	if err != nil {
		return nil, ErrNoSuchComment
	}

	if !subj.CanOn(authz.ActionCommentDelete, comment.AuthorUUID) {
		return nil, ErrNotYourComment
	}

	if err := s.store.SoftDeleteComment(ctx, commentUUID, s.now()); err != nil {
		return nil, err
	}
	comment.CardUUID = card.UUID
	return comment, nil
}

// commentForRouting looks a comment up without any access check.
//
// Unscoped by design, like ProjectOf: `/comments/{x}` carries no project, so this is
// the lookup that lets access be resolved against one. It returns only the card uuid
// the caller needs to do that.
func (s *Service) commentForRouting(ctx context.Context, commentUUID string) (*Comment, error) {
	comment, err := s.store.CommentByUUID(ctx, commentUUID)
	if err != nil {
		return nil, err
	}
	if comment == nil {
		return nil, ErrNoSuchComment
	}
	return comment, nil
}
