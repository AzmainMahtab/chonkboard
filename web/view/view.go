// Package view holds the plain data shapes the templ components render.
// It exists so components never import a domain package: handlers map
// domain -> view, and the UI layer stays a leaf of the dependency graph.
package view

import "time"

// Page is the chrome every full page render needs.
type Page struct {
	Title       string
	CSRFToken   string
	CurrentUser CurrentUser
	AssetSuffix string // cache-busting query for /static, e.g. "?v=a1b2c3"
}

type CurrentUser struct {
	UUID        string
	DisplayName string
	Email       string
	IsSuperUser bool
}

type Card struct {
	UUID      string
	LaneUUID  string
	Title     string
	Priority  string // low | normal | high | urgent
	DueAt     *time.Time
	Assignee  *CurrentUser
	Labels    []Label
	Comments  int
	Files     int
	HasDesc   bool
	UpdatedAt time.Time
}

type Label struct {
	UUID  string
	Name  string
	Color string
}

type Lane struct {
	UUID     string
	Name     string
	Color    string // token suffix: slate | blue | teal | green | amber | rose | violet
	WIPLimit int    // 0 means no limit
	IsDone   bool
	Cards    []Card
}

// OverWIP reports whether the lane is past a limit it actually has.
func (l Lane) OverWIP() bool { return l.WIPLimit > 0 && len(l.Cards) > l.WIPLimit }

type Board struct {
	ProjectUUID string
	// ProjectSlug is what URLs are built from; the uuid is what SSE and fragments
	// carry.
	ProjectSlug string
	ProjectName string
	Lanes       []Lane
	CanManage   bool // manager or super admin: may change the board's shape
	// CanAddCards gates the add-card control. False until phase 4 builds the
	// route behind it — a visible button that 404s is worse than no button, and
	// this is one field to delete rather than markup to restore.
	CanAddCards bool
}

// LoginForm is the login page's state across a failed submission: what the person
// typed (except the password) and what was wrong with it.
type LoginForm struct {
	Email string
	// Error is the form-level message. For a failed sign-in it is deliberately
	// the same whether the address is unknown or the password is wrong.
	Error         string
	EmailError    string
	PasswordError string
}

// PasswordForm is the account page's state.
type PasswordForm struct {
	// MustChange is true when the user is being held here until they choose
	// their own password.
	MustChange   bool
	Done         bool
	Error        string
	CurrentError string
	NewError     string
}

// ProjectSummary is a project as the list page shows it.
type ProjectSummary struct {
	UUID string
	Slug string
	Name string
	// Description is trimmed for the card; the full text lives on the settings page.
	Description string
	// Role is the caller's grant — "manager", "member", or "" for an operator
	// looking at a board they hold no row on.
	Role       string
	IsArchived bool
	Lanes      int
	Cards      int
	UpdatedAt  time.Time
}

// ProjectList is the application's home.
type ProjectList struct {
	Projects []ProjectSummary
	// CanCreate is the operator only.
	CanCreate bool
	// ShowArchived reflects the ?archived=1 toggle.
	ShowArchived bool
	// HasArchived decides whether the toggle is worth rendering at all.
	HasArchived bool
	Form        ProjectForm
}

// ProjectForm is the create/rename form's state across a failed submission.
type ProjectForm struct {
	Name        string
	Slug        string
	Description string
	Open        bool
	Error       string
	NameError   string
	SlugError   string
	DescError   string
}

// Member is one row of the project's people list.
type Member struct {
	UserUUID    string
	DisplayName string
	Email       string
	Role        string // manager | member
	Suspended   bool
	// IsSelf marks the signed-in user, who is not offered a remove button for
	// their own grant.
	IsSelf bool
}

// Candidate is somebody who could be added to a project.
type Candidate struct {
	UUID        string
	DisplayName string
	Email       string
}

// LaneForm is the lane create/edit form's state.
type LaneForm struct {
	UUID   string
	Name   string
	Colour string
	// WIPLimit is the raw field value: "" means no limit.
	WIPLimit string
	IsDone   bool
	// Colours is the palette, passed in so the component never hard-codes it.
	Colours   []string
	Error     string
	NameError string
	WIPError  string
}

// IsNew reports whether this form creates a lane rather than editing one.
func (f LaneForm) IsNew() bool { return f.UUID == "" }

// ProjectSettings is the manager's page for one board.
type ProjectSettings struct {
	Project ProjectSummary
	Lanes   []Lane
	Labels  []Label
	Members []Member
	// Candidates is who is left to add.
	Candidates []Candidate
	// CanGrantManager is the operator only — a manager must not mint peers.
	CanGrantManager bool
	// CanDelete and CanArchive are the operator only.
	CanArchive bool
	CanDelete  bool
	Form       ProjectForm
	// Saved shows a confirmation after a successful save.
	Saved string
}

// CardForm is the create/edit form's state across a failed submission.
type CardForm struct {
	// UUID is empty when the form creates a card.
	UUID        string
	LaneUUID    string
	LaneName    string
	Title       string
	Description string
	Error       string
	TitleError  string
	DescError   string
}

// IsNew reports whether this form creates a card rather than editing one.
func (f CardForm) IsNew() bool { return f.UUID == "" }

// ActivityEntry is one line of a card's history.
type ActivityEntry struct {
	// Kind is the raw kind, so the component can pick a verb and an icon.
	Kind  string
	Actor string
	// From and To are lane names, set only for a move.
	From string
	To   string
	At   time.Time
}

// CardDetail is the card modal.
type CardDetail struct {
	Card     Card
	LaneName string
	// Description is the raw markdown for now; phase 5 renders it.
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	IsArchived  bool
	// CanDelete is false for a member looking at somebody else's card.
	CanDelete bool
	History   []ActivityEntry
}
