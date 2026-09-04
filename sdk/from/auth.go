package from

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Credential is how an HTTP source authenticates, and what the SDK does to
// keep the credential alive. See HTTP.Auth.
type Credential = core.Credential

// Refresh renews a credential that expires, by calling the endpoint that
// reissues it before the first page. See Credential.Refresh.
type Refresh = core.Refresh

// Secret produces the value a source authenticates with. FromEnv covers the
// common case; a login call is just another function of this shape.
type Secret = core.Secret

// Applier puts the secret on the outgoing request. See AsBearer, AsCookie,
// AsCookieNamed and AsHeader.
type Applier = core.Applier

// FromEnv reads the secret from an environment variable, and says which one
// when it is unset -- rather than sending an empty credential and letting the
// API answer 401.
func FromEnv(name string) Secret { return core.FromEnv(name) }

// AsBearer and AsCookie are variables rather than functions so that they are
// assignable to Applier. Declared as funcs they would need an http.Header
// parameter to have the identical type Go requires, which would put net/http
// in the signature of a field most consumers never touch -- and the version
// that reads better, map[string][]string, does not compile at the assignment.
var (
	// AsBearer sends the secret as "Authorization: Bearer <secret>".
	AsBearer Applier = core.AsBearer

	// AsCookie sends the secret as the whole Cookie header, which is what
	// someone who copied a session cookie out of a browser has in hand. For a
	// bare value, AsCookieNamed.
	AsCookie Applier = core.AsCookie
)

// AsCookieNamed sends the secret as the value of one named cookie.
func AsCookieNamed(name string) Applier { return core.AsCookieNamed(name) }

// AsHeader sends the secret as the whole value of a header of your choosing,
// for the APIs that want X-API-Key or similar.
func AsHeader(name string) Applier { return core.AsHeader(name) }

// JSONField reads an RFC 3339 timestamp out of a top-level field of the
// refresh response: {"expires": "2026-10-04T22:15:07.197Z"} is
// JSONField("expires").
func JSONField(name string) func([]byte) (time.Time, error) { return core.JSONField(name) }
