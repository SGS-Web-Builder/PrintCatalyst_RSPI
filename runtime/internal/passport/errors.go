package passport

import "errors"

// Sentinel errors mapped to HTTP status codes by the owner handlers.
//
// ErrInvalid is the catch-all for malformed create-session input and
// composes with %w so the original cause can be unwrapped by the caller.
// ErrNotFound is returned by Get/Remove when the session id does not exist
// or was soft-deleted. ErrUnknownPreset is returned when the preset name is
// not in the known list. ErrBadFaceRegion is returned when the operator
// confirmation is empty, has a negative dimension, or does not overlap the
// source image. ErrCompose is returned when the rendering step cannot
// produce a valid PNG.
var (
	ErrInvalid         = errors.New("passport: invalid input")
	ErrNotFound        = errors.New("passport: not found")
	ErrUnknownPreset   = errors.New("passport: unknown preset")
	ErrBadFaceRegion   = errors.New("passport: face region is empty or out of bounds")
	ErrInvalidBackground = errors.New("passport: unknown background kind")
	ErrCompose         = errors.New("passport: failed to compose output")
	ErrNoDocument      = errors.New("passport: source document missing")
)