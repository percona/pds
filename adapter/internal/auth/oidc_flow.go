package auth

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// OIDCFlowTTL is how long a sign in may take between leaving for the issuer
// and coming back.
const OIDCFlowTTL = 10 * time.Minute

type sealedFlow struct {
	OIDCFlow
	Expires int64 `json:"e"`
}

// SealOIDCFlow returns the cookie value that carries a flow across the
// redirect to the issuer: the flow and its expiry, signed with key. The flow
// is not secret from the browser that started it, but it must not be one the
// browser could have made up.
func SealOIDCFlow(key []byte, flow OIDCFlow, now time.Time) string {
	raw, _ := json.Marshal(sealedFlow{OIDCFlow: flow, Expires: now.Add(OIDCFlowTTL).Unix()})

	return SignSessionID(key, base64.RawURLEncoding.EncodeToString(raw))
}

// OpenOIDCFlow returns the flow a cookie value carries, and reports whether
// it was sealed with key and is still inside its lifetime.
func OpenOIDCFlow(key []byte, value string, now time.Time) (OIDCFlow, bool) {
	body, ok := VerifySessionID(key, value)
	if !ok {
		return OIDCFlow{}, false
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return OIDCFlow{}, false
	}

	var sealed sealedFlow
	if err := json.Unmarshal(raw, &sealed); err != nil || !now.Before(time.Unix(sealed.Expires, 0)) {
		return OIDCFlow{}, false
	}
	if sealed.State == "" || sealed.Nonce == "" || sealed.Verifier == "" {
		return OIDCFlow{}, false
	}

	return sealed.OIDCFlow, true
}
