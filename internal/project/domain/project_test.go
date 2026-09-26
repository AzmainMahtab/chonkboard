package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func TestProjectRolePermissions(t *testing.T) {
	// The requirement the whole permission model exists for: a member drags,
	// creates and edits cards, and can do nothing to the board itself.
	assert.True(t, RoleManager.CanManageBoard())
	assert.False(t, RoleMember.CanManageBoard(),
		"a member must never be able to change the board's shape")
	assert.False(t, ProjectRole("").CanManageBoard())
	assert.False(t, ProjectRole("admin").CanManageBoard())

	assert.True(t, RoleManager.Valid())
	assert.True(t, RoleMember.Valid())
	assert.False(t, ProjectRole("owner").Valid())
}

func TestSlugFromName(t *testing.T) {
	tests := map[string]string{
		"Chonkboard":                "chonkboard",
		"  My Big Project  ":        "my-big-project",
		"Q3 2026 — Roadmap!":        "q3-2026-roadmap",
		"lots   of    space":        "lots-of-space",
		"--leading-and-trailing--":  "leading-and-trailing",
		"Ünïcødé Ñame":              "n-c-d-ame",
		"!!!":                       "",
		"":                          "",
		strings.Repeat("long ", 20): "long-long-long-long-long-long-long-long",
	}
	for in, want := range tests {
		got := SlugFromName(in)
		assert.Equal(t, want, got, "input %q", in)
		if got != "" {
			assert.LessOrEqual(t, len(got), MaxSlugLength)
			assert.Empty(t, validateSlug(got), "derived slug %q must be valid", got)
		}
	}
}

func TestNewProject(t *testing.T) {
	p, err := NewProject("p-1", " CHONK ", "  Chonkboard  ", "  A board.  ", "u-1", testNow)

	require.NoError(t, err)
	assert.Equal(t, "chonk", p.Slug, "folded on the way in")
	assert.Equal(t, "Chonkboard", p.Name)
	assert.Equal(t, "A board.", p.Description)
	assert.False(t, p.IsArchived())
	assert.Equal(t, testNow, p.CreatedAt)
}

func TestNewProjectValidation(t *testing.T) {
	tests := []struct {
		name       string
		uuid       string
		slug       string
		display    string
		desc       string
		createdBy  string
		wantFields []string
	}{
		{
			name:       "everything missing",
			wantFields: []string{"uuid", "slug", "name", "created_by"},
		},
		{
			name: "slug with spaces", uuid: "p", slug: "my project",
			display: "N", createdBy: "u",
			wantFields: []string{"slug"},
		},
		{
			name: "slug with an underscore", uuid: "p", slug: "my_project",
			display: "N", createdBy: "u",
			wantFields: []string{"slug"},
		},
		{
			name: "slug with a double hyphen", uuid: "p", slug: "my--project",
			display: "N", createdBy: "u",
			wantFields: []string{"slug"},
		},
		{
			name: "slug with a leading hyphen", uuid: "p", slug: "-project",
			display: "N", createdBy: "u",
			wantFields: []string{"slug"},
		},
		{
			name: "slug too long", uuid: "p", slug: strings.Repeat("a", MaxSlugLength+1),
			display: "N", createdBy: "u",
			wantFields: []string{"slug"},
		},
		{
			name: "name too long", uuid: "p", slug: "ok",
			display: strings.Repeat("x", MaxNameLength+1), createdBy: "u",
			wantFields: []string{"name"},
		},
		{
			name: "description too long", uuid: "p", slug: "ok", display: "N",
			desc: strings.Repeat("x", MaxDescriptionLength+1), createdBy: "u",
			wantFields: []string{"description"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewProject(tc.uuid, tc.slug, tc.display, tc.desc, tc.createdBy, testNow)

			require.Error(t, err)
			assert.Nil(t, p)
			assert.ErrorIs(t, err, apperrors.New(apperrors.CodeValidation, ""))

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}

func TestProjectRenameAndArchive(t *testing.T) {
	later := testNow.Add(time.Hour)

	t.Run("rename", func(t *testing.T) {
		p, _ := NewProject("p", "chonk", "Before", "", "u", testNow)
		require.NoError(t, p.Rename("  After  ", "  New body.  ", later))
		assert.Equal(t, "After", p.Name)
		assert.Equal(t, "New body.", p.Description)
		assert.Equal(t, later, p.UpdatedAt)

		require.Error(t, p.Rename("", "", later))
		assert.Equal(t, "After", p.Name, "a rejected rename changes nothing")
	})

	t.Run("archive is idempotent and reversible", func(t *testing.T) {
		p, _ := NewProject("p", "chonk", "Chonkboard", "", "u", testNow)

		p.Archive(later)
		require.True(t, p.IsArchived())
		first := *p.ArchivedAt

		p.Archive(later.Add(time.Hour))
		assert.Equal(t, first, *p.ArchivedAt, "the first archive time stands")

		p.Restore(later.Add(2 * time.Hour))
		assert.False(t, p.IsArchived())
		assert.Nil(t, p.ArchivedAt)
	})
}

func TestNewMembership(t *testing.T) {
	m, err := NewMembership("p-1", "u-1", RoleManager, "owner", testNow)

	require.NoError(t, err)
	assert.Equal(t, RoleManager, m.Role)
	assert.Equal(t, testNow, m.GrantedAt)
}

func TestNewMembershipValidation(t *testing.T) {
	tests := []struct {
		name       string
		project    string
		user       string
		role       ProjectRole
		grantedBy  string
		wantFields []string
	}{
		{
			name:       "everything missing",
			wantFields: []string{"project_uuid", "user_uuid", "role", "granted_by"},
		},
		{
			name: "unknown role", project: "p", user: "u",
			role: ProjectRole("admin"), grantedBy: "o",
			wantFields: []string{"role"},
		},
		{
			name: "no granter", project: "p", user: "u", role: RoleMember,
			wantFields: []string{"granted_by"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewMembership(tc.project, tc.user, tc.role, tc.grantedBy, testNow)

			require.Error(t, err)
			assert.Nil(t, m)

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}
