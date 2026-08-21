package model

// Notification is the subset of a GitHub notification thread used by gh-hush.
type Notification struct {
	ID         string     `json:"id"`
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

// Resource is enriched issue, pull request, discussion, or comment data.
type Resource struct {
	HTMLURL            string `json:"html_url"`
	Body               string `json:"body"`
	User               User   `json:"user"`
	Assignees          []User `json:"assignees"`
	RequestedReviewers []User `json:"requested_reviewers"`
	RequestedTeams     []Team `json:"requested_teams"`
}

// User identifies a GitHub user.
type User struct {
	Login string `json:"login"`
}

// Team identifies a GitHub team.
type Team struct {
	Slug string `json:"slug"`
}

// Enrichment contains fetched subject evidence and any uncertainty.
type Enrichment struct {
	Subject       Resource
	LatestComment Resource
	Err           error
}

// Action is a proposed notification subscription decision.
type Action string

const (
	// ActionKeep leaves the notification subscription unchanged.
	ActionKeep Action = "keep"
	// ActionUnsubscribe proposes removing the notification subscription.
	ActionUnsubscribe Action = "unsubscribe"
)

// Rule records an exact policy match and its evidence.
type Rule struct {
	ID       string `json:"id"`
	Evidence string `json:"evidence"`
}

// Decision is one explainable classification.
type Decision struct {
	Thread          Notification
	URL             string
	Action          Action
	Rules           []Rule
	EnrichmentError string
}
