package connectors

import "context"

const CapWorkspaceInspect = "workspace.inspect"

// Codes a Google Workspace connection returns when its keyless sign-in path
// stops short, so callers can tell the person exactly what to fix. They sit
// beside the error's kind (always auth): the connection works again once the
// person signs in, is granted permission to sign as the service account, or
// the tenant's administrator authorizes delegation.
const (
	// The connection signs without a key but nobody has signed it in to the
	// Google account allowed to sign as the service account, or that sign-in
	// was revoked or expired.
	CodeWorkspaceSignerNeeded = "workspace_signer_needed"
	// The signed-in Google account may not sign as the service account
	// (iam.serviceAccounts.signJwt), the service account is not there, or
	// the IAM Service Account Credentials API is off in its project.
	CodeWorkspaceSignerDenied = "workspace_signer_denied"
	// Google refused the delegated token: the tenant has not authorized the
	// service account's client ID for these scopes, or the administrator is
	// not one of the tenant's.
	CodeWorkspaceDelegationDenied = "workspace_delegation_denied"
)

// CodedError is an Error with a more precise code than its kind. errors.As
// still finds the *Error inside, so KindOf and every caller that only knows
// kinds keep working.
type CodedError struct {
	Err  *Error
	code string
}

func (e *CodedError) Error() string { return e.Err.Message }
func (e *CodedError) Unwrap() error { return e.Err }

// Code is the precise code, such as workspace_signer_needed.
func (e *CodedError) Code() string { return e.code }

// Codedf builds a CodedError of kind with a code and a message.
func Codedf(code, kind, format string, a ...any) error {
	return &CodedError{Err: Errf(kind, format, a...), code: code}
}

// WorkspaceDomain is a non-secret observation of a tenant's domain. It does
// not assert Gmail activation, licenses, DNS authentication or mailbox readiness.
type WorkspaceDomain struct {
	CustomerID string `json:"customer_id"`
	Domain     string `json:"domain"`
	Verified   bool   `json:"verified"`
	Primary    bool   `json:"primary"`
}
type WorkspaceInspector interface {
	InspectWorkspaceDomain(context.Context, *Client, Creds, string, string) (WorkspaceDomain, error)
}

// CapWorkspaceManageUsers changes existing users' profiles in a tenant the
// workspace connected: names, photos and Gmail signatures. It never creates,
// deletes or suspends a user, and never reads or sets a password.
const CapWorkspaceManageUsers = "workspace.manage_users"

// CodeWorkspaceWritesOff means the connection was not set up to change users:
// the person has to turn on profile changes and authorize the write scopes.
const CodeWorkspaceWritesOff = "workspace_writes_off"

// WorkspaceUserUpdate is one user's profile change. A nil field is left as it
// is. Photo is JPEG or PNG bytes; PhotoType is "JPEG" or "PNG".
type WorkspaceUserUpdate struct {
	Email      string
	GivenName  *string
	FamilyName *string
	Photo      []byte
	PhotoType  string
	Signature  *string
}

// WorkspaceUserResult says, per part of an update, "updated", "failed" or
// "" (not asked for). It never carries Google's messages.
type WorkspaceUserResult struct {
	Name      string `json:"name,omitempty"`
	Photo     string `json:"photo,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type WorkspaceUserManager interface {
	UpdateWorkspaceUser(context.Context, *Client, Creds, WorkspaceUserUpdate) (WorkspaceUserResult, error)
}
