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
	// ReturnTo is where Save and Cancel go: "board" when the form was opened from
	// the board, otherwise the project's settings page.
	//
	// Only those two values are ever honoured, and the path is built server-side from
	// them — a form field carrying a URL is an open redirect waiting to be found.
	ReturnTo string

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
	// LabelForm is the create-or-edit form for a project label.
	LabelForm LabelForm
	Form      ProjectForm
	// Saved shows a confirmation after a successful save.
	Saved string
}

// CardForm is the create/edit form's state across a failed submission.
//
// Every field is submitted every time, so there is no "leave unchanged" case: an
// absent field and a cleared field would otherwise be indistinguishable, and clearing
// a due date would be impossible.
type CardForm struct {
	// UUID is empty when the form creates a card.
	UUID        string
	LaneUUID    string
	LaneName    string
	Title       string
	Description string

	Priority string
	// Assignee is a user uuid, or "" for nobody.
	Assignee string
	// DueAt is the raw yyyy-mm-dd field value, or "" for no due date.
	DueAt string
	// LabelUUIDs is which labels are ticked.
	LabelUUIDs []string

	// Priorities, People and Labels are the choices the form offers.
	Priorities []Choice
	People     []Candidate
	Labels     []Label

	Error         string
	TitleError    string
	DescError     string
	DueError      string
	AssigneeError string
}

// Choice is one option in a select.
type Choice struct {
	Value string
	Label string
}

// HasLabel reports whether a label is ticked, for rendering the checkbox.
func (f CardForm) HasLabel(uuid string) bool {
	for _, u := range f.LabelUUIDs {
		if u == uuid {
			return true
		}
	}
	return false
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
	// DescriptionHTML is the description already rendered from markdown and
	// sanitised. It is trusted markup by the time it reaches a component, which is
	// why the field name says so — the sanitising happens in the handler, not here.
	DescriptionHTML string
	// HasDescription distinguishes an empty body from one that sanitised to nothing.
	HasDescription bool
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	IsArchived     bool
	// CanDelete is false for a member looking at somebody else's card.
	CanDelete bool
	History   []ActivityEntry

	Comments    []Comment
	Attachments []Attachment
	// MoveTo is the other lanes on this board, for the keyboard move control.
	MoveTo []Choice
	// Priority is the human label for the card's priority, or "" for none.
	Priority string
}

// Comment is one entry in a card's discussion.
type Comment struct {
	UUID   string
	Author string
	// BodyHTML is rendered and sanitised, like a description.
	BodyHTML  string
	CreatedAt time.Time
	// IsDeleted marks a tombstone: the row stays so a thread does not silently lose
	// its shape and replies above it stop making sense.
	IsDeleted bool
	CanDelete bool
}

// Attachment is one file on a card.
type Attachment struct {
	UUID string
	// Filename is what the uploader called it. Never a path, and never what the
	// bytes are stored under.
	Filename   string
	SizeHuman  string
	MIMEType   string
	UploadedBy string
	CreatedAt  time.Time
	CanDelete  bool
	// IsImage decides whether the panel shows a thumbnail.
	IsImage bool
}

// LabelForm is the project-label form's state.
type LabelForm struct {
	// UUID is empty when the form creates a label.
	UUID   string
	Name   string
	Colour string
	// Colours is the palette.
	Colours   []string
	Error     string
	NameError string
}

// IsNew reports whether this form creates a label rather than editing one.
func (f LabelForm) IsNew() bool { return f.UUID == "" }

// ---------------------------------------------------------------------------
// The admin console. The operator's surface.
// ---------------------------------------------------------------------------

// Account is one account as the console lists it.
type Account struct {
	UUID        string
	DisplayName string
	Email       string
	IsOperator  bool
	Suspended   bool
	// MustChangePassword marks somebody who has been given a credential and has not
	// yet replaced it — the console shows it, because it is how the operator knows a
	// handover has not completed.
	MustChangePassword bool
	// Sessions is how many live sessions they hold, so the console shows who is
	// signed in right now.
	Sessions int
	// Projects is how many boards they were granted.
	Projects int
	// IsSelf marks the signed-in operator, who is not offered controls that would
	// lock them out.
	IsSelf    bool
	CreatedAt time.Time
}

// Reveal is a one-time password shown exactly once.
//
// It exists only for the length of the response that created it. It is never stored,
// never put in a redirect, and never logged — which is why it is a field on a page
// model rather than anything that survives a request.
type Reveal struct {
	Email    string
	Password string
	// Reset distinguishes the wording: a new account versus a replaced credential.
	Reset bool
}

// AdminProject is one project as the console lists it.
type AdminProject struct {
	UUID       string
	Slug       string
	Name       string
	IsArchived bool
	Members    int
	UpdatedAt  time.Time
	// Access is who can reach it.
	Access []Member
}

// AccountForm is the create/edit form's state.
type AccountForm struct {
	// UUID is empty when the form creates an account.
	UUID        string
	DisplayName string
	Email       string
	IsOperator  bool
	Error       string
	NameError   string
	EmailError  string
}

// IsNew reports whether this form creates an account rather than editing one.
func (f AccountForm) IsNew() bool { return f.UUID == "" }

// AdminPage is the console.
type AdminPage struct {
	Accounts []Account
	Projects []AdminProject
	// Operators is how many owner accounts can still sign in. One is worth warning
	// about: there is nobody to reset the password if it is lost.
	Operators int
	// Reveal carries a one-time password, set only on the response that generated it.
	Reveal *Reveal
	Form   AccountForm
	// ShowArchived reflects the ?archived=1 toggle.
	ShowArchived bool
	Saved        string
}
