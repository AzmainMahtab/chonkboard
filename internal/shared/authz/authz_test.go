package authz_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	projectdomain "github.com/AzmainMahtab/chonkboard/internal/project/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authz"
)

// The four standings a request can have. Everything below is a matrix over these.
func operator() authz.Subject {
	return authz.NewSubject(
		&authdomain.User{UUID: "op", Role: authdomain.RoleSuperAdmin, Status: authdomain.StatusActive},
		nil, // the operator needs no grant; access is global
	)
}

func manager() authz.Subject {
	role := projectdomain.RoleManager
	return authz.NewSubject(
		&authdomain.User{UUID: "mgr", Role: authdomain.RoleMember, Status: authdomain.StatusActive},
		&role,
	)
}

func member() authz.Subject {
	role := projectdomain.RoleMember
	return authz.NewSubject(
		&authdomain.User{UUID: "mem", Role: authdomain.RoleMember, Status: authdomain.StatusActive},
		&role,
	)
}

// stranger is signed in but has no grant on this board.
func stranger() authz.Subject {
	return authz.NewSubject(
		&authdomain.User{UUID: "str", Role: authdomain.RoleMember, Status: authdomain.StatusActive},
		nil,
	)
}

func anonymous() authz.Subject { return authz.Subject{} }

// TestPermissionMatrix is the requirement, expressed as a table.
//
// Every row of the matrix in .project-doc/docs/AUTH_FLOWS.md appears here with a
// yes/no for all four standings. If this test and that document ever disagree, one
// of them is a bug.
func TestPermissionMatrix(t *testing.T) {
	type row struct {
		action                                    authz.Action
		operator, manager, member, stranger, anon bool
	}

	// Read the columns as: operator | manager | member | stranger | anonymous.
	matrix := []row{
		// --- Installation administration: the operator alone. ---
		{authz.ActionProjectCreate, true, false, false, false, false},
		{authz.ActionProjectDelete, true, false, false, false, false},
		{authz.ActionProjectArchive, true, false, false, false, false},
		{authz.ActionUserCreate, true, false, false, false, false},
		{authz.ActionUserResetPassword, true, false, false, false, false},
		{authz.ActionUserSuspend, true, false, false, false, false},
		{authz.ActionUserListAll, true, false, false, false, false},
		{authz.ActionProjectListAll, true, false, false, false, false},
		// A manager must not be able to mint peers.
		{authz.ActionMemberGrantManager, true, false, false, false, false},

		// --- The shape of the board: manager or above. ---
		// These are the rows the brief is explicit about: a member cannot
		// change any setting of the board.
		{authz.ActionProjectRename, true, true, false, false, false},
		{authz.ActionLaneManage, true, true, false, false, false},
		{authz.ActionLabelManage, true, true, false, false, false},
		{authz.ActionMemberGrant, true, true, false, false, false},

		// --- The work on the board: any granted member. ---
		// Also the brief verbatim: they make cards, edit cards, drag cards.
		{authz.ActionProjectView, true, true, true, false, false},
		{authz.ActionCardCreate, true, true, true, false, false},
		{authz.ActionCardEdit, true, true, true, false, false},
		{authz.ActionCardMove, true, true, true, false, false},
		{authz.ActionCardArchive, true, true, true, false, false},
		{authz.ActionCommentCreate, true, true, true, false, false},
		{authz.ActionAttachmentUpload, true, true, true, false, false},
		{authz.ActionCardLabelAttach, true, true, true, false, false},

		// --- Ownership-sensitive: Can() is the "anyone's work" question. ---
		// A member gets false here and true from CanOn for their own work.
		{authz.ActionCardDelete, true, true, false, false, false},
		{authz.ActionCommentDelete, true, true, false, false, false},
		{authz.ActionAttachmentDelete, true, true, false, false, false},

		// --- Anyone signed in. ---
		{authz.ActionPasswordChangeOwn, true, true, true, true, false},
	}

	subjects := []struct {
		name string
		subj authz.Subject
		pick func(row) bool
	}{
		{"operator", operator(), func(r row) bool { return r.operator }},
		{"project manager", manager(), func(r row) bool { return r.manager }},
		{"project member", member(), func(r row) bool { return r.member }},
		{"signed in, no grant", stranger(), func(r row) bool { return r.stranger }},
		{"anonymous", anonymous(), func(r row) bool { return r.anon }},
	}

	for _, r := range matrix {
		for _, s := range subjects {
			t.Run(fmt.Sprintf("%s/%s", r.action, s.name), func(t *testing.T) {
				assert.Equal(t, s.pick(r), s.subj.Can(r.action))
			})
		}
	}
}

// TestEveryActionAppearsInTheMatrix guards against a new Action being added to the
// package without a row in the test above — which would otherwise be granted or
// refused by the fall-through with nobody having decided.
func TestEveryActionAppearsInTheMatrix(t *testing.T) {
	all := []authz.Action{
		authz.ActionProjectCreate, authz.ActionProjectDelete, authz.ActionProjectArchive,
		authz.ActionProjectRename, authz.ActionLaneManage, authz.ActionLabelManage,
		authz.ActionMemberGrant, authz.ActionMemberGrantManager,
		authz.ActionUserCreate, authz.ActionUserResetPassword, authz.ActionUserSuspend,
		authz.ActionUserListAll, authz.ActionProjectListAll,
		authz.ActionProjectView, authz.ActionCardCreate, authz.ActionCardEdit,
		authz.ActionCardMove, authz.ActionCardArchive, authz.ActionCardDelete,
		authz.ActionCommentCreate, authz.ActionCommentDelete,
		authz.ActionAttachmentUpload, authz.ActionAttachmentDelete,
		authz.ActionCardLabelAttach, authz.ActionPasswordChangeOwn,
	}
	assert.Len(t, all, 25, "an action was added or removed; update the matrix test")

	// The operator can do everything, so this doubles as a check that no action
	// falls through to the default refusal by accident.
	for _, a := range all {
		assert.True(t, operator().Can(a), "the operator should be able to %s", a)
	}
}

func TestUnknownActionIsRefused(t *testing.T) {
	// A typo in an action constant must deny, not allow. The operator is exempt
	// by design -- they can do everything -- so this is checked on a manager.
	assert.False(t, manager().Can(authz.Action("lane.manag")))
	assert.False(t, member().Can(authz.Action("")))
}

func TestCanOnAllowsAMemberOnlyTheirOwnWork(t *testing.T) {
	for _, action := range []authz.Action{
		authz.ActionCardDelete, authz.ActionCommentDelete, authz.ActionAttachmentDelete,
	} {
		t.Run(string(action), func(t *testing.T) {
			m := member()

			assert.True(t, m.CanOn(action, "mem"), "their own")
			assert.False(t, m.CanOn(action, "someone-else"), "another person's")
			assert.False(t, m.CanOn(action, ""), "an unknown owner must not pass")

			assert.True(t, manager().CanOn(action, "anyone"), "a manager may act on any")
			assert.True(t, operator().CanOn(action, "anyone"), "so may the operator")

			assert.False(t, stranger().CanOn(action, "str"),
				"no grant means no access even to your own work on that board")
			assert.False(t, anonymous().CanOn(action, "anyone"))
		})
	}
}

func TestCanOnIsCanForActionsThatAreNotOwnershipSensitive(t *testing.T) {
	// So a handler can call CanOn unconditionally and stay correct.
	m := member()
	assert.True(t, m.CanOn(authz.ActionCardEdit, "someone-else"))
	assert.False(t, m.CanOn(authz.ActionLaneManage, "mem"),
		"ownership must not unlock a board setting")
}

func TestASuspendedAccountCanDoNothing(t *testing.T) {
	// The session middleware refuses a suspended user on every request; this is
	// the second line, in case a path ever reaches authz without it.
	suspended := &authdomain.User{
		UUID: "op", Role: authdomain.RoleSuperAdmin, Status: authdomain.StatusSuspended,
	}
	role := projectdomain.RoleManager

	subj := authz.NewSubject(suspended, &role)

	assert.False(t, subj.Can(authz.ActionProjectView))
	assert.False(t, subj.Can(authz.ActionCardMove))
	assert.False(t, subj.Can(authz.ActionPasswordChangeOwn))
	assert.False(t, subj.Can(authz.ActionProjectCreate),
		"not even a suspended super admin")
	assert.False(t, subj.CanOn(authz.ActionCardDelete, "op"))
}

func TestAnInvalidProjectRoleGrantsNothing(t *testing.T) {
	// A role string that is not in the schema's CHECK should never appear, but if
	// one did it must not be treated as a grant.
	bogus := projectdomain.ProjectRole("owner")
	subj := authz.NewSubject(
		&authdomain.User{UUID: "u", Role: authdomain.RoleMember, Status: authdomain.StatusActive},
		&bogus,
	)

	assert.False(t, subj.Can(authz.ActionProjectView))
	assert.False(t, subj.Can(authz.ActionCardMove))
	assert.False(t, subj.Can(authz.ActionLaneManage))
}

func TestNamedHelpersMatchTheirActions(t *testing.T) {
	// CanManageBoard is the phrase the requirement uses, so it is worth asserting
	// it means what the routes assume.
	require.True(t, manager().CanManageBoard())
	require.False(t, member().CanManageBoard())
	require.True(t, operator().CanManageBoard())

	require.True(t, member().CanSeeProject())
	require.False(t, stranger().CanSeeProject())
}

func TestTheOperatorNeedsNoGrant(t *testing.T) {
	// Their access is global; there is no project_members row for them.
	op := operator()
	require.Nil(t, op.ProjectRole)
	assert.True(t, op.CanSeeProject())
	assert.True(t, op.CanManageBoard())
}
