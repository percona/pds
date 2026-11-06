package cursor

import "errors"

var (
	// ErrUnauthorized means the key was refused.
	ErrUnauthorized = errors.New("cursor: unauthorized")
	// ErrRejected means Cursor refused the request for another reason.
	ErrRejected = errors.New("cursor: rejected")
)
