// Package authz resolves every permission question in the application.
//
// The matrix here **is** the requirement: the operator is the only administrator,
// project managers own the shape of their board, and members create, edit and drag
// cards and can do nothing else. Handlers call this; they never re-derive a rule
// inline, because a rule expressed in two places eventually disagrees with itself.
//
// Corresponding documentation: .project-doc/docs/AUTH_FLOWS.md.
package authz

import (
	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	projectdomain "github.com/AzmainMahtab/chonkboard/internal/project/domain"
)

// Action is something a person might try to do.
type Action string

// Project-shape actions. Every one of these is refused to a member.
const (
	// ActionProjectCreate creates a board. Operator only.
	ActionProjectCreate Action = "project.create"
	// ActionProjectDelete permanently removes a board and everything on it.
	ActionProjectDelete Action = "project.delete"
	// ActionProjectArchive hides a board reversibly.
	ActionProjectArchive Action = "project.archive"
	// ActionProjectRename edits a board's name and description.
	ActionProjectRename Action = "project.rename"
	// ActionLaneManage covers adding, renaming, recolouring, reordering and
	// deleting a lane, and setting or clearing its WIP limit. One action rather
	// than six, because no role is ever granted a subset.
	ActionLaneManage Action = "lane.manage"
	// ActionLabelManage covers creating, editing and deleting a project's labels.
	ActionLabelManage Action = "label.manage"
	// ActionMemberGrant grants or revokes ordinary member access.
	ActionMemberGrant Action = "member.grant"
	// ActionMemberGrantManager promotes someone to project manager. Operator
	// only: a manager must not be able to mint peers.
	ActionMemberGrantManager Action = "member.grant_manager"
)

// Account actions. Operator only — there is no self-service anything.
const (
	// ActionUserCreate creates an account and sets its initial password.
	ActionUserCreate Action = "user.create"
	// ActionUserResetPassword resets somebody else's password.
	ActionUserResetPassword Action = "user.reset_password"
	// ActionUserSuspend suspends or reinstates an account.
	ActionUserSuspend Action = "user.suspend"
	// ActionUserListAll sees every account.
	ActionUserListAll Action = "user.list_all"
	// ActionProjectListAll sees every board, granted or not.
	ActionProjectListAll Action = "project.list_all"
)

// Card actions. These are what a member is here to do.
const (
	// ActionProjectView opens a board.
	ActionProjectView Action = "project.view"
	// ActionCardCreate adds a card.
	ActionCardCreate Action = "card.create"
	// ActionCardEdit edits any of a card's fields.
	ActionCardEdit Action = "card.edit"
	// ActionCardMove drags a card, within a lane or across lanes.
	ActionCardMove Action = "card.move"
	// ActionCardArchive archives or restores a card.
	ActionCardArchive Action = "card.archive"
	// ActionCardDelete deletes a card. Ownership-sensitive — use CanOn.
	ActionCardDelete Action = "card.delete"
	// ActionCommentCreate comments on a card.
	ActionCommentCreate Action = "comment.create"
	// ActionCommentDelete deletes a comment. Ownership-sensitive.
	ActionCommentDelete Action = "comment.delete"
	// ActionAttachmentUpload attaches a file.
	ActionAttachmentUpload Action = "attachment.upload"
	// ActionAttachmentDelete removes an attachment. Ownership-sensitive.
	ActionAttachmentDelete Action = "attachment.delete"
	// ActionCardLabelAttach attaches or detaches an existing project label.
	ActionCardLabelAttach Action = "card.label"
	// ActionPasswordChangeOwn changes one's own password. Everyone signed in.
	ActionPasswordChangeOwn Action = "password.change_own"
)

// ownershipSensitive are the actions where a member may act on their own work and
// nothing else. Can() refuses them for a member; CanOn() is what allows them.
var ownershipSensitive = map[Action]bool{
	ActionCardDelete:       true,
	ActionCommentDelete:    true,
	ActionAttachmentDelete: true,
}

// Subject is who is asking, and what standing they have on the board in question.
type Subject struct {
	// User is the signed-in account. nil means anonymous, which can do nothing.
	User *authdomain.User
	// ProjectRole is their grant on the board being acted on. nil means no
	// grant — absence is denial, so there is no negative grant to consider.
	ProjectRole *projectdomain.ProjectRole
}

// NewSubject builds a subject from a user and their optional project grant.
func NewSubject(user *authdomain.User, role *projectdomain.ProjectRole) Subject {
	return Subject{User: user, ProjectRole: role}
}

// isOperator reports whether this is the super admin.
//
// A suspended account is never an operator, and never anything else either: the
// session middleware refuses a suspended user on every request, and this is the
// second line in case a code path ever reaches here without it.
func (s Subject) isOperator() bool {
	return s.User != nil && s.User.CanSignIn() && s.User.IsSuperAdmin()
}

// isManager reports whether this subject manages the board in question.
func (s Subject) isManager() bool {
	return s.signedIn() && s.ProjectRole != nil && s.ProjectRole.CanManageBoard()
}

// isMember reports whether this subject was granted the board at all.
func (s Subject) isMember() bool {
	return s.signedIn() && s.ProjectRole != nil && s.ProjectRole.Valid()
}

func (s Subject) signedIn() bool {
	return s.User != nil && s.User.CanSignIn()
}

// Can answers a permission question that does not depend on who owns the thing.
//
// For the three ownership-sensitive actions it returns true only for a manager or
// the operator; a member must go through CanOn.
func (s Subject) Can(action Action) bool {
	if s.User == nil || !s.User.CanSignIn() {
		return false
	}

	// The operator can do everything. Stated once, here, rather than as a
	// special case in twenty branches.
	if s.isOperator() {
		return true
	}

	switch action {
	// Operator only. A manager runs a board; they do not administer the
	// installation or mint other managers.
	case ActionProjectCreate,
		ActionProjectDelete,
		ActionProjectArchive,
		ActionMemberGrantManager,
		ActionUserCreate,
		ActionUserResetPassword,
		ActionUserSuspend,
		ActionUserListAll,
		ActionProjectListAll:
		return false

	// Manager or above: the shape of the board.
	case ActionProjectRename,
		ActionLaneManage,
		ActionLabelManage,
		ActionMemberGrant:
		return s.isManager()

	// Any granted member: the work on the board. These are the brief verbatim.
	case ActionProjectView,
		ActionCardCreate,
		ActionCardEdit,
		ActionCardMove,
		ActionCardArchive,
		ActionCommentCreate,
		ActionAttachmentUpload,
		ActionCardLabelAttach:
		return s.isMember()

	// Ownership-sensitive: a manager may act on anything on their board, a
	// member only on their own, which Can cannot know about.
	case ActionCardDelete,
		ActionCommentDelete,
		ActionAttachmentDelete:
		return s.isManager()

	// Anyone signed in.
	case ActionPasswordChangeOwn:
		return true
	}

	// An unknown action is refused. A new action must be added to the matrix and
	// to its test before it grants anything.
	return false
}

// CanOn answers an ownership-sensitive question: may this subject act on a thing
// created by ownerUUID?
//
// A member may delete their own card, comment or attachment and nobody else's. For
// any action that is not ownership-sensitive this is exactly Can, so a handler can
// call CanOn unconditionally and stay correct.
func (s Subject) CanOn(action Action, ownerUUID string) bool {
	if s.Can(action) {
		return true
	}
	if !ownershipSensitive[action] {
		return false
	}
	return s.isMember() && s.User != nil && ownerUUID != "" && s.User.UUID == ownerUUID
}

// CanSeeProject reports whether this subject may open a board at all. The
// operator sees every board without needing a grant; everybody else needs one.
func (s Subject) CanSeeProject() bool { return s.Can(ActionProjectView) }

// CanManageBoard is the single question the board-settings routes ask. It exists
// as a named method because that phrase is the requirement, and a reader should
// not have to know which Action constant encodes it.
func (s Subject) CanManageBoard() bool { return s.Can(ActionLaneManage) }
