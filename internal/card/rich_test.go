package card_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/board"
	boarddomain "github.com/AzmainMahtab/chonkboard/internal/board/domain"
	"github.com/AzmainMahtab/chonkboard/internal/card"
	"github.com/AzmainMahtab/chonkboard/internal/card/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

// label creates a project label and returns its uuid.
func (f *serviceFixture) label(t *testing.T, name string, colour boarddomain.Colour) string {
	t.Helper()
	l, err := f.board.CreateLabel(f.ctx, f.asManager(), f.project,
		board.LabelInput{Name: name, Colour: colour})
	require.NoError(t, err)
	return l.UUID
}

func TestEditSavesEveryRichField(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")
	bug := f.label(t, "bug", boarddomain.ColourRose)
	chore := f.label(t, "chore", boarddomain.ColourTeal)

	assignee := f.member.UUID
	due := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)

	saved, err := f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID, card.EditInput{
		Title:        "Ship the thing",
		Description:  "A **markdown** body.",
		Priority:     domain.PriorityUrgent,
		AssigneeUUID: &assignee,
		DueAt:        &due,
		LabelUUIDs:   []string{bug, chore},
	})
	require.NoError(t, err)

	assert.Equal(t, "Ship the thing", saved.Title)
	assert.Equal(t, "A **markdown** body.", saved.Description)
	assert.Equal(t, domain.PriorityUrgent, saved.Priority)
	require.NotNil(t, saved.AssigneeUUID)
	assert.Equal(t, f.member.UUID, *saved.AssigneeUUID)
	require.NotNil(t, saved.DueAt)
	assert.True(t, due.Equal(*saved.DueAt))

	labels, err := f.svc.LabelUUIDsForCards(f.ctx, []string{c.UUID})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{bug, chore}, labels[c.UUID])

	// And it all survives a re-read.
	reloaded, err := f.svc.ByUUID(f.ctx, f.asMember(), f.project, c.UUID)
	require.NoError(t, err)
	assert.Equal(t, domain.PriorityUrgent, reloaded.Priority)
	require.NotNil(t, reloaded.DueAt)
	assert.True(t, due.Equal(*reloaded.DueAt))
}

func TestEditClearsEveryOptionalField(t *testing.T) {
	// The form submits every field every time, which is what makes clearing one
	// expressible at all — an absent field and a cleared field would otherwise look
	// the same.
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")
	bug := f.label(t, "bug", boarddomain.ColourRose)

	assignee := f.member.UUID
	due := time.Now().Add(48 * time.Hour)
	_, err := f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID, card.EditInput{
		Title: "a card", Priority: domain.PriorityHigh,
		AssigneeUUID: &assignee, DueAt: &due, LabelUUIDs: []string{bug},
	})
	require.NoError(t, err)

	cleared, err := f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID, card.EditInput{
		Title: "a card", Priority: domain.PriorityNone,
	})
	require.NoError(t, err)

	assert.Nil(t, cleared.AssigneeUUID)
	assert.Nil(t, cleared.DueAt)
	assert.Equal(t, domain.PriorityNone, cleared.Priority)

	labels, err := f.svc.LabelUUIDsForCards(f.ctx, []string{c.UUID})
	require.NoError(t, err)
	assert.Empty(t, labels[c.UUID], "an empty label set clears every label")
}

func TestAnEmptyPriorityBecomesNone(t *testing.T) {
	// A form that omits the field, or a create with nothing chosen, must not write
	// an empty string into a column with a CHECK on it.
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	saved, err := f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID,
		card.EditInput{Title: "a card"})

	require.NoError(t, err)
	assert.Equal(t, domain.PriorityNone, saved.Priority)
}

func TestAnAssigneeMustHaveAccessToTheBoard(t *testing.T) {
	// The foreign key would catch an unknown uuid. This catches a *known* person
	// with no grant, who would otherwise be assigned work they cannot see.
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	stranger := f.addUser(t, "stranger@example.com", authRoleMember())
	f.svc.UseMembers(stubMembers{allowed: map[string]bool{f.member.UUID: true}})

	ok := f.member.UUID
	_, err := f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID, card.EditInput{
		Title: "a card", AssigneeUUID: &ok,
	})
	assert.NoError(t, err, "a member of the board is fine")

	notOK := stranger.UUID
	_, err = f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID, card.EditInput{
		Title: "a card", AssigneeUUID: &notOK,
	})
	require.Error(t, err)
	assert.Equal(t, apperrors.CodeValidation, apperrors.From(err).Code)
	require.NotEmpty(t, apperrors.From(err).Fields)
	assert.Equal(t, "assignee", apperrors.From(err).Fields[0].Field)
}

func TestALabelFromAnotherBoardCannotBeAttached(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	other := f.otherProject(t)
	foreign, err := f.board.CreateLabel(f.ctx, f.asOperator(), other,
		board.LabelInput{Name: "theirs", Colour: boarddomain.ColourBlue})
	require.NoError(t, err)

	_, err = f.svc.Edit(f.ctx, f.asMember(), f.project, c.UUID, card.EditInput{
		Title: "a card", LabelUUIDs: []string{foreign.UUID},
	})

	require.Error(t, err)
	assert.Equal(t, apperrors.CodeNotFound, apperrors.From(err).Code)
}

func TestCommentLifecycle(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	comment, err := f.svc.Comment(f.ctx, f.asMember(), f.project, c.UUID, "  A comment.  ")
	require.NoError(t, err)
	assert.Equal(t, "A comment.", comment.Body, "trimmed")
	assert.False(t, comment.IsDeleted())

	comments, err := f.svc.Comments(f.ctx, f.asMember(), f.project, c.UUID)
	require.NoError(t, err)
	require.Len(t, comments, 1)

	counts, err := f.svc.CommentCounts(f.ctx, []string{c.UUID})
	require.NoError(t, err)
	assert.Equal(t, 1, counts[c.UUID])

	// A comment is worth an activity row: it is a thing that happened to the card.
	history, err := f.svc.History(f.ctx, f.asMember(), f.project, c.UUID, 10)
	require.NoError(t, err)
	assert.Equal(t, card.ActivityCommented, history[0].Kind)
}

func TestCommentValidation(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	for name, body := range map[string]string{
		"empty":      "",
		"whitespace": "   \n\t  ",
		"too long":   strings.Repeat("x", card.MaxCommentLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Comment(f.ctx, f.asMember(), f.project, c.UUID, body)
			require.Error(t, err)
			assert.Equal(t, apperrors.CodeValidation, apperrors.From(err).Code)
		})
	}
}

func TestADeletedCommentLeavesATombstone(t *testing.T) {
	// A thread that silently loses a message leaves the replies above it making no
	// sense.
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")
	comment, err := f.svc.Comment(f.ctx, f.asMember(), f.project, c.UUID, "A comment.")
	require.NoError(t, err)

	_, err = f.svc.DeleteComment(f.ctx, f.asMember(), f.project, comment.UUID)
	require.NoError(t, err)

	comments, err := f.svc.Comments(f.ctx, f.asMember(), f.project, c.UUID)
	require.NoError(t, err)
	require.Len(t, comments, 1, "the row stays")
	assert.True(t, comments[0].IsDeleted())

	counts, err := f.svc.CommentCounts(f.ctx, []string{c.UUID})
	require.NoError(t, err)
	assert.Zero(t, counts[c.UUID], "but it does not count towards the card's badge")

	// Deleting twice is not an error: a double-submitted form must not fail.
	_, err = f.svc.DeleteComment(f.ctx, f.asMember(), f.project, comment.UUID)
	assert.NoError(t, err)
}

func TestAMemberDeletesOnlyTheirOwnComment(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	theirs, err := f.svc.Comment(f.ctx, f.asMember(), f.project, c.UUID, "the member's")
	require.NoError(t, err)
	somebodyElses, err := f.svc.Comment(f.ctx, f.asOperator(), f.project, c.UUID, "the owner's")
	require.NoError(t, err)

	_, err = f.svc.DeleteComment(f.ctx, f.asMember(), f.project, somebodyElses.UUID)
	assert.ErrorIs(t, err, card.ErrNotYourComment)

	_, err = f.svc.DeleteComment(f.ctx, f.asMember(), f.project, theirs.UUID)
	assert.NoError(t, err)

	// A manager may clear up anybody's.
	_, err = f.svc.DeleteComment(f.ctx, f.asManager(), f.project, somebodyElses.UUID)
	assert.NoError(t, err)
}

func TestACommentOnAnotherBoardIsInvisible(t *testing.T) {
	f := newService(t)
	other := f.otherProject(t)
	otherLanes, err := f.board.Lanes(f.ctx, f.asOperator(), other)
	require.NoError(t, err)
	theirCard, err := f.svc.Create(f.ctx, f.asOperator(), other, otherLanes[0].UUID, "theirs")
	require.NoError(t, err)
	theirComment, err := f.svc.Comment(f.ctx, f.asOperator(), other, theirCard.UUID, "theirs")
	require.NoError(t, err)

	_, err = f.svc.DeleteComment(f.ctx, f.asManager(), f.project, theirComment.UUID)
	assert.ErrorIs(t, err, card.ErrNoSuchComment)
}

func TestAttachLifecycle(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	attachment, err := f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, card.UploadInput{
		Filename: "notes.csv", MIMEType: "text/csv",
		Content: strings.NewReader("a,b\n1,2\n"),
	})
	require.NoError(t, err)

	assert.Equal(t, "notes.csv", attachment.Filename)
	assert.Equal(t, "text/csv", attachment.MIMEType)
	assert.Equal(t, int64(8), attachment.SizeBytes)
	assert.NotEqual(t, attachment.Filename, attachment.StoredName,
		"the bytes must not be stored under the uploader's name")
	assert.Len(t, attachment.StoredName, 32)

	files, err := f.svc.Attachments(f.ctx, f.asMember(), f.project, c.UUID)
	require.NoError(t, err)
	require.Len(t, files, 1)

	counts, err := f.svc.AttachmentCounts(f.ctx, []string{c.UUID})
	require.NoError(t, err)
	assert.Equal(t, 1, counts[c.UUID])

	// And the bytes come back.
	_, file, err := f.svc.OpenAttachment(f.ctx, f.asMember(), f.project, attachment.UUID)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	buf := make([]byte, 8)
	n, _ := file.Read(buf)
	assert.Equal(t, "a,b\n1,2\n", string(buf[:n]))
}

func TestAttachRefusesAnOversizedFile(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	_, err := f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, card.UploadInput{
		Filename: "big.png", MIMEType: "image/png",
		Content: strings.NewReader(strings.Repeat("x", (64<<10)+1)),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, card.ErrUploadTooLarge)
	assert.Equal(t, 413, apperrors.From(err).Status())

	files, err := f.svc.Attachments(f.ctx, f.asMember(), f.project, c.UUID)
	require.NoError(t, err)
	assert.Empty(t, files, "no row for a refused upload")
}

func TestAttachEnforcesAnAllowlist(t *testing.T) {
	// An allowlist, not a denylist: a denylist is a guess at what is dangerous and
	// is always incomplete.
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	refused := map[string]card.UploadInput{
		"a shell script":  {Filename: "x.sh", MIMEType: "application/x-sh"},
		"an executable":   {Filename: "x.exe", MIMEType: "application/x-msdownload"},
		"html":            {Filename: "x.html", MIMEType: "text/html"},
		"svg":             {Filename: "x.svg", MIMEType: "image/svg+xml"},
		"an unknown type": {Filename: "x.bin", MIMEType: "application/octet-stream"},
		"no filename":     {Filename: "", MIMEType: "text/csv"},
		"a long filename": {Filename: strings.Repeat("a", 300) + ".csv", MIMEType: "text/csv"},
	}
	for name, in := range refused {
		t.Run(name, func(t *testing.T) {
			in.Content = strings.NewReader("x")
			_, err := f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, in)
			require.Error(t, err)
			assert.Equal(t, apperrors.CodeValidation, apperrors.From(err).Code)
		})
	}

	allowed := map[string]card.UploadInput{
		"a png":                  {Filename: "x.png", MIMEType: "image/png"},
		"a pdf":                  {Filename: "x.pdf", MIMEType: "application/pdf"},
		"csv":                    {Filename: "x.csv", MIMEType: "text/csv"},
		"a docx":                 {Filename: "x.docx", MIMEType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		"a type with parameters": {Filename: "x.csv", MIMEType: "text/csv; charset=utf-8"},
		"an upper-case type":     {Filename: "x.png", MIMEType: "IMAGE/PNG"},
	}
	for name, in := range allowed {
		t.Run(name, func(t *testing.T) {
			in.Content = strings.NewReader("x")
			_, err := f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, in)
			assert.NoError(t, err)
		})
	}
}

func TestAFilenameWithAPathIsReducedToItsBase(t *testing.T) {
	// Some browsers send a full path. What is stored for display has to be a name.
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	for _, given := range []string{
		"/home/person/secret/report.pdf",
		"../../../../etc/report.pdf",
		"subdir/report.pdf",
	} {
		t.Run(given, func(t *testing.T) {
			a, err := f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, card.UploadInput{
				Filename: given, MIMEType: "application/pdf",
				Content: strings.NewReader("x"),
			})
			require.NoError(t, err)
			assert.Equal(t, "report.pdf", a.Filename)
			assert.NotContains(t, a.Filename, "/")
		})
	}
}

func TestAnAttachmentOnAnotherBoardIsNotFound(t *testing.T) {
	// 404, never 403: a leaked URL must not confirm the file exists.
	f := newService(t)
	other := f.otherProject(t)
	otherLanes, err := f.board.Lanes(f.ctx, f.asOperator(), other)
	require.NoError(t, err)
	theirCard, err := f.svc.Create(f.ctx, f.asOperator(), other, otherLanes[0].UUID, "theirs")
	require.NoError(t, err)
	theirs, err := f.svc.Attach(f.ctx, f.asOperator(), other, theirCard.UUID, card.UploadInput{
		Filename: "secret.pdf", MIMEType: "application/pdf", Content: strings.NewReader("x"),
	})
	require.NoError(t, err)

	t.Run("open", func(t *testing.T) {
		_, _, err := f.svc.OpenAttachment(f.ctx, f.asManager(), f.project, theirs.UUID)
		assert.ErrorIs(t, err, card.ErrNoSuchAttachment)
	})
	t.Run("delete", func(t *testing.T) {
		_, err := f.svc.DeleteAttachment(f.ctx, f.asManager(), f.project, theirs.UUID)
		assert.ErrorIs(t, err, card.ErrNoSuchAttachment)
	})
	t.Run("a made-up id looks the same", func(t *testing.T) {
		_, _, err := f.svc.OpenAttachment(
			f.ctx, f.asManager(), f.project, idgenerator.NewUUIDv7())
		assert.ErrorIs(t, err, card.ErrNoSuchAttachment)
	})
}

func TestAMemberDeletesOnlyTheirOwnAttachment(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	theirs, err := f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, card.UploadInput{
		Filename: "mine.csv", MIMEType: "text/csv", Content: strings.NewReader("x"),
	})
	require.NoError(t, err)
	somebodyElses, err := f.svc.Attach(f.ctx, f.asOperator(), f.project, c.UUID, card.UploadInput{
		Filename: "theirs.csv", MIMEType: "text/csv", Content: strings.NewReader("x"),
	})
	require.NoError(t, err)

	_, err = f.svc.DeleteAttachment(f.ctx, f.asMember(), f.project, somebodyElses.UUID)
	assert.ErrorIs(t, err, card.ErrNotYourAttachment)

	_, err = f.svc.DeleteAttachment(f.ctx, f.asMember(), f.project, theirs.UUID)
	assert.NoError(t, err)

	_, err = f.svc.DeleteAttachment(f.ctx, f.asManager(), f.project, somebodyElses.UUID)
	assert.NoError(t, err, "a manager may clear up anybody's")
}

func TestDeletingAnAttachmentRemovesItsBytes(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")
	a, err := f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, card.UploadInput{
		Filename: "x.csv", MIMEType: "text/csv", Content: strings.NewReader("x"),
	})
	require.NoError(t, err)

	_, err = f.svc.DeleteAttachment(f.ctx, f.asMember(), f.project, a.UUID)
	require.NoError(t, err)

	_, _, err = f.svc.OpenAttachment(f.ctx, f.asMember(), f.project, a.UUID)
	assert.ErrorIs(t, err, card.ErrNoSuchAttachment)
}

func TestMoveToLaneIsTheSameRulesAsADrag(t *testing.T) {
	f := newService(t)
	resident := f.add(t, f.asMember(), 1, "resident")
	moving := f.add(t, f.asMember(), 0, "moving")
	staying := f.add(t, f.asMember(), 0, "staying")

	affected, err := f.svc.MoveToLane(f.ctx, f.asMember(), f.project, moving.UUID, f.lane(1))

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{f.lane(0), f.lane(1)}, affected)
	assert.Equal(t, []string{"staying"}, f.order(t, 0))
	assert.Equal(t, []string{"resident", "moving"}, f.order(t, 1),
		"appended to the end of the target")
	_ = resident
	_ = staying
}

func TestMoveToLaneRespectsAWIPLimit(t *testing.T) {
	f := newService(t)
	f.setWIP(t, 1, 1)
	f.add(t, f.asMember(), 1, "resident")
	moving := f.add(t, f.asMember(), 0, "moving")

	_, err := f.svc.MoveToLane(f.ctx, f.asMember(), f.project, moving.UUID, f.lane(1))

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrWIPExceeded)
	assert.Equal(t, []string{"moving"}, f.order(t, 0), "nothing moved")
}

func TestMoveToLaneToWhereItAlreadyIsDoesNothing(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "a card")

	affected, err := f.svc.MoveToLane(f.ctx, f.asMember(), f.project, c.UUID, f.lane(0))

	require.NoError(t, err)
	assert.Empty(t, affected)
	assert.Equal(t, []string{"a card"}, f.order(t, 0))
}

func TestCommentsAndAttachmentsCascadeWithTheirCard(t *testing.T) {
	f := newService(t)
	c := f.add(t, f.asMember(), 0, "going")
	_, err := f.svc.Comment(f.ctx, f.asMember(), f.project, c.UUID, "a comment")
	require.NoError(t, err)
	_, err = f.svc.Attach(f.ctx, f.asMember(), f.project, c.UUID, card.UploadInput{
		Filename: "x.csv", MIMEType: "text/csv", Content: strings.NewReader("x"),
	})
	require.NoError(t, err)

	_, err = f.svc.Delete(f.ctx, f.asManager(), f.project, c.UUID)
	require.NoError(t, err)

	var comments, attachments int
	require.NoError(t, f.db.Reader().GetContext(f.ctx, &comments,
		`SELECT count(*) FROM comments WHERE card_uuid = ?`, c.UUID))
	require.NoError(t, f.db.Reader().GetContext(f.ctx, &attachments,
		`SELECT count(*) FROM attachments WHERE card_uuid = ?`, c.UUID))
	assert.Zero(t, comments)
	assert.Zero(t, attachments)
}
