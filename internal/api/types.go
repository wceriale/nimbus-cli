package api

import "time"

// DeviceCode starts a device-flow login (RFC 8628 §3.2).
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Token is a successful /oauth/token response.
type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// OAuthError is an error response from an /oauth endpoint (RFC 6749 §5.2).
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

type Me struct {
	Email string `json:"email"`
	// Domain is the verified domain the identity provider vouches for, ""
	// for an account without one, such as Gmail.
	Domain string `json:"domain"`
	// Org is nil until the account's first deploy creates its org, or, for a
	// work account, until someone from its domain has deployed.
	Org *Org   `json:"org"`
	IdP string `json:"idp"`
}

// Org kinds.
const (
	OrgPersonal = "personal"
	OrgWork     = "work"
)

// Org is a workspace (a work domain) or a personal space.
type Org struct {
	ID              int64  `json:"id"`
	Key             string `json:"key"` // what nimbus.yaml's org: holds: the domain (work) or the slug (personal)
	Slug            string `json:"slug"`
	Kind            string `json:"kind"`   // personal or work
	Domain          string `json:"domain"` // "" for a personal org
	Name            string `json:"name"`
	Role            string `json:"role"` // owner or member
	ExternalSharing bool   `json:"external_sharing"`
	Plan            string `json:"plan"`
	AppsUsed        int    `json:"apps_used"`
	AppsLimit       *int   `json:"apps_limit"` // nil when there's no limit
}

// App states: new, building, live, failed.
type App struct {
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	Owner         string    `json:"owner"`
	State         string    `json:"state"`
	LiveVersion   *int      `json:"live_version"`
	LatestVersion *int      `json:"latest_version"`
	SharedWith    []string  `json:"shared_with"`
	Public        bool      `json:"public"`
	Org           string    `json:"org"`        // the org's key, as nimbus.yaml's org: holds it
	OrgKind       string    `json:"org_kind"`   // personal or work
	Visibility    string    `json:"visibility"` // private, shared, or public
	SharesUsed    int       `json:"shares_used"`
	SharesLimit   *int      `json:"shares_limit"` // nil when there's no limit
	AppsUsed      int       `json:"apps_used"`    // apps in the org, this one included
	AppsLimit     *int      `json:"apps_limit"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Version states: queued, building, deploying, live, failed, retired.
type Version struct {
	Number       int        `json:"number"`
	State        string     `json:"state"`
	SourceSHA256 string     `json:"source_sha256"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Error        *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint,omitempty"`
	} `json:"error,omitempty"`
	LogTail []string `json:"log_tail,omitempty"`
}

// Upload says where to send a source tarball.
type Upload struct {
	UploadID        string            `json:"upload_id"`
	AlreadyUploaded bool              `json:"already_uploaded"`
	UploadURL       string            `json:"upload_url"`
	UploadMethod    string            `json:"upload_method"`
	UploadHeaders   map[string]string `json:"upload_headers"`
}

type BuildLog struct {
	Version   int      `json:"version"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

type LogEntry struct {
	Time     time.Time `json:"time"`
	Severity string    `json:"severity"`
	Message  string    `json:"message"`
	Version  int       `json:"version"`
}

type Logs struct {
	Entries   []LogEntry `json:"entries"`
	Truncated bool       `json:"truncated"`
}

type Shares struct {
	App         string   `json:"app"`
	URL         string   `json:"url"`
	SharedWith  []string `json:"shared_with"`
	Public      bool     `json:"public"`
	Visibility  string   `json:"visibility"`
	SharesUsed  int      `json:"shares_used"`
	SharesLimit *int     `json:"shares_limit"`
}

// OrgUpdate is the answer to a change of an org's settings.
type OrgUpdate struct {
	Org           Org `json:"org"`
	RemovedShares int `json:"removed_shares"`
}
