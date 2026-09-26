package project

import (
	"context"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Store reads and writes the projects and project_members tables.
type Store struct {
	tx *database.TxManager
}

// NewStore wires a store to the transaction manager.
func NewStore(tx *database.TxManager) *Store { return &Store{tx: tx} }

const projectColumns = `
	uuid, slug, name, description, created_by, created_at, updated_at, archived_at`

type projectModel struct {
	UUID        string            `db:"uuid"`
	Slug        string            `db:"slug"`
	Name        string            `db:"name"`
	Description string            `db:"description"`
	CreatedBy   string            `db:"created_by"`
	CreatedAt   database.Time     `db:"created_at"`
	UpdatedAt   database.Time     `db:"updated_at"`
	ArchivedAt  database.NullTime `db:"archived_at"`
}

func (m projectModel) toDomain() *domain.Project {
	return &domain.Project{
		UUID:        m.UUID,
		Slug:        m.Slug,
		Name:        m.Name,
		Description: m.Description,
		CreatedBy:   m.CreatedBy,
		CreatedAt:   m.CreatedAt.Time(),
		UpdatedAt:   m.UpdatedAt.Time(),
		ArchivedAt:  m.ArchivedAt.Ptr(),
	}
}

func toProjectModel(p *domain.Project) projectModel {
	return projectModel{
		UUID:        p.UUID,
		Slug:        p.Slug,
		Name:        p.Name,
		Description: p.Description,
		CreatedBy:   p.CreatedBy,
		CreatedAt:   database.NewTime(p.CreatedAt),
		UpdatedAt:   database.NewTime(p.UpdatedAt),
		ArchivedAt:  database.NullTimeFrom(p.ArchivedAt),
	}
}

func toDomainProjects(models []projectModel) []*domain.Project {
	out := make([]*domain.Project, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out
}

// Create inserts a project.
func (s *Store) Create(ctx context.Context, p *domain.Project) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO projects (`+projectColumns+`)
		VALUES (:uuid, :slug, :name, :description, :created_by,
		        :created_at, :updated_at, :archived_at)`,
		toProjectModel(p))
	if err != nil {
		if database.IsUniqueViolation(err) {
			return apperrors.Conflict("a project already uses that key").
				WithField("slug", "is already taken").
				Wrap(err)
		}
		return database.MapError(err, "create project")
	}
	return nil
}

// ByUUID returns the project, or (nil, nil) when there is none.
func (s *Store) ByUUID(ctx context.Context, uuid string) (*domain.Project, error) {
	return s.one(ctx, `SELECT `+projectColumns+` FROM projects WHERE uuid = :uuid`,
		map[string]any{"uuid": uuid})
}

// BySlug returns the project with that URL key, or (nil, nil).
func (s *Store) BySlug(ctx context.Context, slug string) (*domain.Project, error) {
	return s.one(ctx, `SELECT `+projectColumns+` FROM projects WHERE slug = :slug`,
		map[string]any{"slug": domain.NormaliseSlug(slug)})
}

func (s *Store) one(ctx context.Context, query string, args map[string]any) (*domain.Project, error) {
	var m projectModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m, query, args)
	if err != nil || !found {
		return nil, err
	}
	return m.toDomain(), nil
}

// ListAll returns every project, for the operator.
func (s *Store) ListAll(ctx context.Context, includeArchived bool) ([]*domain.Project, error) {
	query := `SELECT ` + projectColumns + ` FROM projects`
	if !includeArchived {
		query += ` WHERE archived_at IS NULL`
	}
	query += ` ORDER BY name, uuid`

	var models []projectModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, query, map[string]any{}); err != nil {
		return nil, err
	}
	return toDomainProjects(models), nil
}

// ListForUser returns only the projects a user was granted.
//
// The join to project_members is the access rule itself, applied in the query
// rather than filtered afterwards: a project the caller has no grant for never
// reaches Go, so there is no code path that could forget to check.
func (s *Store) ListForUser(ctx context.Context, userUUID string, includeArchived bool) ([]*domain.Project, error) {
	query := `
		SELECT ` + prefixed(projectColumns, "p.") + `
		FROM projects p
		JOIN project_members m ON m.project_uuid = p.uuid
		WHERE m.user_uuid = :user_uuid`
	if !includeArchived {
		query += ` AND p.archived_at IS NULL`
	}
	query += ` ORDER BY p.name, p.uuid`

	var models []projectModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, query,
		map[string]any{"user_uuid": userUUID}); err != nil {
		return nil, err
	}
	return toDomainProjects(models), nil
}

// Update writes the mutable fields back.
func (s *Store) Update(ctx context.Context, p *domain.Project) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE projects SET
			slug        = :slug,
			name        = :name,
			description = :description,
			updated_at  = :updated_at,
			archived_at = :archived_at
		WHERE uuid = :uuid`,
		toProjectModel(p))
	if err != nil {
		if database.IsUniqueViolation(err) {
			return apperrors.Conflict("a project already uses that key").
				WithField("slug", "is already taken").
				Wrap(err)
		}
		return database.MapError(err, "update project")
	}
	return database.RequireRow(n, "that project no longer exists")
}

// Delete removes a project. Lanes, cards, labels, grants and activity all
// cascade, so this genuinely erases the board — archiving is the reversible
// option.
func (s *Store) Delete(ctx context.Context, uuid string) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx),
		`DELETE FROM projects WHERE uuid = :uuid`, map[string]any{"uuid": uuid})
	if err != nil {
		return database.MapError(err, "delete project")
	}
	return database.RequireRow(n, "that project no longer exists")
}

const membershipColumns = `project_uuid, user_uuid, role, granted_by, granted_at`

type membershipModel struct {
	ProjectUUID string        `db:"project_uuid"`
	UserUUID    string        `db:"user_uuid"`
	Role        string        `db:"role"`
	GrantedBy   string        `db:"granted_by"`
	GrantedAt   database.Time `db:"granted_at"`
}

func (m membershipModel) toDomain() *domain.Membership {
	return &domain.Membership{
		ProjectUUID: m.ProjectUUID,
		UserUUID:    m.UserUUID,
		Role:        domain.ProjectRole(m.Role),
		GrantedBy:   m.GrantedBy,
		GrantedAt:   m.GrantedAt.Time(),
	}
}

// Grant creates or updates a membership.
//
// ON CONFLICT DO UPDATE is the portable upsert: both SQLite (3.24+) and
// PostgreSQL accept it, including the excluded pseudo-table. Re-granting to
// change someone's role is the common case, so making it an error would just push
// a read-then-write race into the caller.
func (s *Store) Grant(ctx context.Context, m *domain.Membership) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO project_members (`+membershipColumns+`)
		VALUES (:project_uuid, :user_uuid, :role, :granted_by, :granted_at)
		ON CONFLICT (project_uuid, user_uuid) DO UPDATE SET
			role       = excluded.role,
			granted_by = excluded.granted_by,
			granted_at = excluded.granted_at`,
		membershipModel{
			ProjectUUID: m.ProjectUUID,
			UserUUID:    m.UserUUID,
			Role:        string(m.Role),
			GrantedBy:   m.GrantedBy,
			GrantedAt:   database.NewTime(m.GrantedAt),
		})
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Invalid("that project or person no longer exists").Wrap(err)
		}
		return database.MapError(err, "grant project access")
	}
	return nil
}

// Revoke removes a membership.
func (s *Store) Revoke(ctx context.Context, projectUUID, userUUID string) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		DELETE FROM project_members
		WHERE project_uuid = :project_uuid AND user_uuid = :user_uuid`,
		map[string]any{"project_uuid": projectUUID, "user_uuid": userUUID})
	if err != nil {
		return database.MapError(err, "revoke project access")
	}
	return database.RequireRow(n, "that person is not a member of this project")
}

// Membership returns a person's grant on one project, or (nil, nil) if they have
// none. This is the authorisation lookup every project route makes.
func (s *Store) Membership(ctx context.Context, projectUUID, userUUID string) (*domain.Membership, error) {
	var m membershipModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m, `
		SELECT `+membershipColumns+` FROM project_members
		WHERE project_uuid = :project_uuid AND user_uuid = :user_uuid`,
		map[string]any{"project_uuid": projectUUID, "user_uuid": userUUID})
	if err != nil || !found {
		return nil, err
	}
	return m.toDomain(), nil
}

// Members lists everyone granted a project.
func (s *Store) Members(ctx context.Context, projectUUID string) ([]*domain.Membership, error) {
	var models []membershipModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models, `
		SELECT `+membershipColumns+` FROM project_members
		WHERE project_uuid = :project_uuid
		ORDER BY granted_at, user_uuid`,
		map[string]any{"project_uuid": projectUUID}); err != nil {
		return nil, err
	}
	out := make([]*domain.Membership, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out, nil
}

// CountManagers reports how many managers a project has. Demoting or removing the
// last one would leave a board nobody but the operator can configure, so callers
// check this first.
func (s *Store) CountManagers(ctx context.Context, projectUUID string) (int, error) {
	var n int
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &n, `
		SELECT count(*) FROM project_members
		WHERE project_uuid = :project_uuid AND role = 'manager'`,
		map[string]any{"project_uuid": projectUUID})
	if err != nil || !found {
		return 0, err
	}
	return n, nil
}

// TouchedAt records that something inside the project changed, so a project list
// can be ordered by recent activity without scanning cards.
func (s *Store) TouchedAt(ctx context.Context, projectUUID string, at time.Time) error {
	_, err := database.ExecNamed(ctx, s.tx.Writer(ctx),
		`UPDATE projects SET updated_at = :at WHERE uuid = :uuid`,
		map[string]any{"at": database.NewTime(at), "uuid": projectUUID})
	return database.MapError(err, "touch project")
}

// prefixed qualifies a projection with a table alias, so the column list stays
// written once even in a join.
func prefixed(columns, alias string) string {
	out := make([]byte, 0, len(columns)*2)
	field := make([]byte, 0, 32)
	flush := func() {
		if len(field) == 0 {
			return
		}
		out = append(out, alias...)
		out = append(out, field...)
		field = field[:0]
	}
	for i := 0; i < len(columns); i++ {
		switch c := columns[i]; c {
		case ',', ' ', '\n', '\t':
			flush()
			out = append(out, c)
		default:
			field = append(field, c)
		}
	}
	flush()
	return string(out)
}
