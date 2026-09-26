package card

import (
	"context"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/filestore"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// MaxFilenameLength bounds the name kept for display. Filesystems stop at 255; this
// value is about what fits in a header and a card panel.
const MaxFilenameLength = 200

// The failures attachments report.
var (
	// ErrNoSuchAttachment covers an attachment that does not exist, one on another
	// board, and one whose bytes have gone missing.
	//
	// **404, never 403.** A leaked attachment URL must not confirm that the file
	// exists — the response to somebody without access has to be
	// indistinguishable from the response to a made-up id.
	ErrNoSuchAttachment = apperrors.NotFound("No such attachment.")

	// ErrNotYourAttachment is what a member gets for somebody else's upload.
	ErrNotYourAttachment = apperrors.Forbidden("You can only delete your own attachments.")

	// ErrUploadTooLarge is returned with a readable message rather than by aborting
	// the connection.
	ErrUploadTooLarge = apperrors.TooLarge("That file is too large.")
)

// allowedMIME is what may be uploaded.
//
// An allowlist, not a denylist: a denylist is a guess at what is dangerous, and it is
// always incomplete. Everything here is something a browser either renders inertly or
// offers to download, and every response carries Content-Disposition: attachment and
// nosniff regardless, so even a mislabelled file is not executed in place.
var allowedMIME = map[string]struct{}{
	"image/png":        {},
	"image/jpeg":       {},
	"image/gif":        {},
	"image/webp":       {},
	"image/avif":       {},
	"application/pdf":  {},
	"text/plain":       {},
	"text/csv":         {},
	"text/markdown":    {},
	"application/json": {},
	"application/zip":  {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   {},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {},
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {},
}

// Attachment is a file on a card.
type Attachment struct {
	UUID       string
	CardUUID   string
	UploadedBy string
	// Filename is what the uploader called it. Display and Content-Disposition
	// only — it never reaches the filesystem.
	Filename string
	// StoredName is the generated name the bytes live under.
	StoredName string
	MIMEType   string
	SizeBytes  int64
	CreatedAt  time.Time
}

const attachmentColumns = `
	uuid, card_uuid, uploaded_by, filename, stored_name, mime_type, size_bytes, created_at`

type attachmentModel struct {
	UUID       string        `db:"uuid"`
	CardUUID   string        `db:"card_uuid"`
	UploadedBy string        `db:"uploaded_by"`
	Filename   string        `db:"filename"`
	StoredName string        `db:"stored_name"`
	MIMEType   string        `db:"mime_type"`
	SizeBytes  int64         `db:"size_bytes"`
	CreatedAt  database.Time `db:"created_at"`
}

func (m attachmentModel) toDomain() Attachment {
	return Attachment{
		UUID: m.UUID, CardUUID: m.CardUUID, UploadedBy: m.UploadedBy,
		Filename: m.Filename, StoredName: m.StoredName, MIMEType: m.MIMEType,
		SizeBytes: m.SizeBytes, CreatedAt: m.CreatedAt.Time(),
	}
}

// CreateAttachment inserts an attachment row.
func (s *Store) CreateAttachment(ctx context.Context, a Attachment) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO attachments (`+attachmentColumns+`)
		VALUES (:uuid, :card_uuid, :uploaded_by, :filename, :stored_name,
		        :mime_type, :size_bytes, :created_at)`,
		attachmentModel{
			UUID: a.UUID, CardUUID: a.CardUUID, UploadedBy: a.UploadedBy,
			Filename: a.Filename, StoredName: a.StoredName, MIMEType: a.MIMEType,
			SizeBytes: a.SizeBytes, CreatedAt: database.NewTime(a.CreatedAt),
		})
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid("That card or person no longer exists.").Wrap(err)
		}
		return database.MapError(err, "create attachment")
	}
	return nil
}

// AttachmentByUUID returns one attachment, or (nil, nil) when there is none.
func (s *Store) AttachmentByUUID(ctx context.Context, uuid string) (*Attachment, error) {
	var m attachmentModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m,
		`SELECT `+attachmentColumns+` FROM attachments WHERE uuid = :uuid`,
		map[string]any{"uuid": uuid})
	if err != nil || !found {
		return nil, err
	}
	a := m.toDomain()
	return &a, nil
}

// AttachmentsForCard returns a card's files, oldest first.
func (s *Store) AttachmentsForCard(ctx context.Context, cardUUID string) ([]Attachment, error) {
	var models []attachmentModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT `+attachmentColumns+` FROM attachments
		WHERE card_uuid = :card_uuid
		ORDER BY created_at, uuid`,
		map[string]any{"card_uuid": cardUUID}); err != nil {
		return nil, err
	}

	out := make([]Attachment, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out, nil
}

// CountAttachmentsForCards returns how many files each card has, in one query.
func (s *Store) CountAttachmentsForCards(ctx context.Context, cardUUIDs []string) (map[string]int, error) {
	if len(cardUUIDs) == 0 {
		return map[string]int{}, nil
	}

	type row struct {
		CardUUID string `db:"card_uuid"`
		N        int    `db:"n"`
	}
	var rows []row
	if err := database.SelectIn(ctx, s.tx.Reader(ctx), &rows, `
		SELECT card_uuid, count(*) AS n FROM attachments
		WHERE card_uuid IN (?)
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

// DeleteAttachment removes the row.
func (s *Store) DeleteAttachment(ctx context.Context, uuid string) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx),
		`DELETE FROM attachments WHERE uuid = :uuid`, map[string]any{"uuid": uuid})
	if err != nil {
		return database.MapError(err, "delete attachment")
	}
	return database.RequireRow(n, "That attachment no longer exists.")
}

// FileStore is what this slice needs to keep attachment bytes.
//
// Declared here, by the consumer, so the card slice depends on four methods rather
// than on a disk implementation — and so a test can use a temporary directory without
// any of this caring.
type FileStore interface {
	Save(name string, r io.Reader, maxBytes int64) (int64, error)
	Open(name string) (*os.File, error)
	Remove(name string) error
}

// UploadInput is a file arriving on a card.
type UploadInput struct {
	Filename string
	// MIMEType is what the client claimed. Checked against the allowlist and then
	// against the declared extension; the stored value is what we decided, not what
	// was claimed.
	MIMEType string
	Content  io.Reader
}

// Attach stores a file against a card.
//
// The bytes land first and the row second. If the row fails the bytes are removed; the
// other order would allow a row pointing at nothing, which is a broken download rather
// than a missing one.
func (s *Service) Attach(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string, in UploadInput,
) (*Attachment, error) {
	if !subj.Can(authz.ActionAttachmentUpload) {
		return nil, apperrors.Forbidden("You cannot attach files on this board.")
	}
	if s.files == nil {
		return nil, apperrors.Internal(errors.New("no file store configured"), "attach file")
	}

	card, err := s.ByUUID(ctx, subj, projectUUID, cardUUID)
	if err != nil {
		return nil, err
	}

	filename, mimeType, err := checkUpload(in)
	if err != nil {
		return nil, err
	}

	storedName, err := filestore.NewName()
	if err != nil {
		return nil, apperrors.Internal(err, "attach file")
	}

	size, err := s.files.Save(storedName, in.Content, s.maxUploadBytes)
	if err != nil {
		if errors.Is(err, filestore.ErrTooLarge) {
			return nil, ErrUploadTooLarge
		}
		return nil, apperrors.Internal(err, "store file")
	}

	attachment := Attachment{
		UUID: idgenerator.NewUUIDv7(), CardUUID: card.UUID,
		UploadedBy: subj.User.UUID, Filename: filename, StoredName: storedName,
		MIMEType: mimeType, SizeBytes: size, CreatedAt: s.now(),
	}

	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.CreateAttachment(ctx, attachment); err != nil {
			return err
		}
		return s.record(ctx, card, ActivityAttached, nil, nil, subj.User.UUID)
	})
	if err != nil {
		// The row did not land, so nothing references these bytes.
		if removeErr := s.files.Remove(storedName); removeErr != nil {
			s.log.ErrorContext(ctx, "orphaned upload after a failed insert",
				"stored_name", storedName, "error", removeErr)
		}
		return nil, err
	}
	return &attachment, nil
}

// Attachments returns a card's files.
func (s *Service) Attachments(
	ctx context.Context, subj authz.Subject, projectUUID, cardUUID string,
) ([]Attachment, error) {
	if _, err := s.ByUUID(ctx, subj, projectUUID, cardUUID); err != nil {
		return nil, err
	}
	return s.store.AttachmentsForCard(ctx, cardUUID)
}

// OpenAttachment authorises an attachment and returns its bytes.
//
// Authorised per request against the card's project, which is what makes a leaked URL
// not a leaked file. Every refusal is ErrNoSuchAttachment — a 403 would confirm the
// file exists, and the URL carries no project to make that harmless.
//
// The caller closes the returned file.
func (s *Service) OpenAttachment(
	ctx context.Context, subj authz.Subject, projectUUID, attachmentUUID string,
) (*Attachment, *os.File, error) {
	attachment, err := s.store.AttachmentByUUID(ctx, attachmentUUID)
	if err != nil {
		return nil, nil, err
	}
	if attachment == nil {
		return nil, nil, ErrNoSuchAttachment
	}

	// Through the card, so an attachment on a board the caller has no grant on
	// resolves to nothing.
	if _, err := s.ByUUID(ctx, subj, projectUUID, attachment.CardUUID); err != nil {
		return nil, nil, ErrNoSuchAttachment
	}

	f, err := s.files.Open(attachment.StoredName)
	if err != nil {
		if errors.Is(err, filestore.ErrNotFound) {
			// The row exists and the bytes do not. That is our bug, but there is
			// nothing the person can do, so it is reported as missing.
			s.log.ErrorContext(ctx, "attachment row with no bytes",
				"attachment", attachment.UUID, "stored_name", attachment.StoredName)
			return nil, nil, ErrNoSuchAttachment
		}
		return nil, nil, apperrors.Internal(err, "open attachment")
	}
	return attachment, f, nil
}

// ProjectOfAttachment returns the project an attachment belongs to.
//
// Unscoped, like ProjectOf: `/attachments/{x}` carries no project, so this is the
// lookup that lets access be resolved against one.
func (s *Service) ProjectOfAttachment(ctx context.Context, attachmentUUID string) (string, error) {
	attachment, err := s.store.AttachmentByUUID(ctx, attachmentUUID)
	if err != nil {
		return "", err
	}
	if attachment == nil {
		return "", ErrNoSuchAttachment
	}
	return s.ProjectOf(ctx, attachment.CardUUID)
}

// DeleteAttachment removes a file and its row.
//
// The row goes first, then the bytes: a row with no bytes is a broken download, while
// bytes with no row are invisible and get cleaned up on the next attempt.
func (s *Service) DeleteAttachment(
	ctx context.Context, subj authz.Subject, projectUUID, attachmentUUID string,
) (*Attachment, error) {
	attachment, err := s.store.AttachmentByUUID(ctx, attachmentUUID)
	if err != nil {
		return nil, err
	}
	if attachment == nil {
		return nil, ErrNoSuchAttachment
	}

	if _, err := s.ByUUID(ctx, subj, projectUUID, attachment.CardUUID); err != nil {
		return nil, ErrNoSuchAttachment
	}
	if !subj.CanOn(authz.ActionAttachmentDelete, attachment.UploadedBy) {
		return nil, ErrNotYourAttachment
	}

	if err := s.store.DeleteAttachment(ctx, attachmentUUID); err != nil {
		return nil, err
	}
	if err := s.files.Remove(attachment.StoredName); err != nil {
		// The row is gone, so the file is unreachable. Log it rather than fail the
		// request: the person's intent was satisfied.
		s.log.ErrorContext(ctx, "cannot remove attachment bytes",
			"stored_name", attachment.StoredName, "error", err)
	}
	return attachment, nil
}

// checkUpload validates a filename and settles on a MIME type.
func checkUpload(in UploadInput) (filename, mimeType string, err error) {
	// filepath.Base strips any directory the browser included — some send a full
	// path — so what is stored for display is a name and not a path.
	filename = strings.TrimSpace(filepath.Base(in.Filename))
	switch {
	case filename == "" || filename == "." || filename == string(filepath.Separator):
		return "", "", apperrors.Validation("That file has no name.").
			WithField("file", "needs a filename")
	case utf8.RuneCountInString(filename) > MaxFilenameLength:
		return "", "", apperrors.Validation("That filename is too long.").
			WithField("file", "has too long a name")
	}

	// The claimed type, with any parameters dropped, folded to lower case.
	claimed := in.MIMEType
	if parsed, _, parseErr := mime.ParseMediaType(claimed); parseErr == nil {
		claimed = parsed
	}
	claimed = strings.ToLower(strings.TrimSpace(claimed))

	// Fall back to the extension when the client said nothing useful. Browsers send
	// application/octet-stream for anything they do not recognise.
	if claimed == "" || claimed == "application/octet-stream" {
		if byExt, _, parseErr := mime.ParseMediaType(
			mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))),
		); parseErr == nil {
			claimed = strings.ToLower(byExt)
		}
	}

	if _, ok := allowedMIME[claimed]; !ok {
		return "", "", apperrors.Validation("That kind of file cannot be attached.").
			WithField("file", "must be an image, a PDF, a document, a text file or a zip")
	}
	return filename, claimed, nil
}
