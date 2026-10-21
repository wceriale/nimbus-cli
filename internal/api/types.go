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
	Org   Org    `json:"org"`
	IdP   string `json:"idp"`
}

type Org struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
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
	App        string   `json:"app"`
	URL        string   `json:"url"`
	SharedWith []string `json:"shared_with"`
}
