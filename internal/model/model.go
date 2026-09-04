package model

// Notification is the subset of a GitHub notification thread used by gh-hush.
type Notification struct {
	ID         string     `json:"id"`
	Unread     bool       `json:"unread"`
	Reason     string     `json:"reason"`
	UpdatedAt  string     `json:"updated_at"`
	Repository Repository `json:"repository"`
	Subject    Subject    `json:"subject"`
}

// Repository identifies the notification repository.
type Repository struct {
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
}

// Subject identifies the resource that generated the notification.
type Subject struct {
	Title            string `json:"title"`
	URL              string `json:"url"`
	LatestCommentURL string `json:"latest_comment_url"`
	Type             string `json:"type"`
}

// Resource is the small common subset of subject and comment API responses
// needed to classify notifications. GitHub calls an Issue/PR/Discussion owner
// "user" and a Commit/Release owner "author".
type Resource struct {
	HTMLURL            string `json:"html_url"`
	Body               string `json:"body"`
	State              string `json:"state"`
	User               User   `json:"user"`
	Author             User   `json:"author"`
	Assignees          []User `json:"assignees"`
	RequestedReviewers []User `json:"requested_reviewers"`
	RequestedTeams     []Team `json:"requested_teams"`
}

type User struct {
	Login string `json:"login"`
}

type Team struct {
	Slug string `json:"slug"`
}

// Action is a proposed notification decision.
type Action string

const (
	ActionKeep                   Action = "keep"
	ActionUnsubscribeAndMarkDone Action = "unsubscribe_and_mark_done"
)

type Rule struct {
	ID       string `json:"id"`
	Evidence string `json:"evidence"`
}

type Decision struct {
	Thread          Notification
	URL             string
	Action          Action
	Rules           []Rule
	EnrichmentError string
}
