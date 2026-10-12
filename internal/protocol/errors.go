package protocol

import (
	"errors"
	"fmt"
	"net/http"

	"impamind/internal/auth"
	"impamind/internal/store"
)

// Error is a protocol error with a stable code and an HTTP status.
type Error struct {
	Code   string `json:"code"`
	Status int    `json:"-"`
	Msg    string `json:"message"`
	Hint   string `json:"hint,omitempty"`
}

func (e *Error) Error() string { return e.Msg }

func newErr(status int, code, msg string, a ...any) *Error {
	return &Error{Code: code, Status: status, Msg: fmt.Sprintf(msg, a...)}
}

func (e *Error) withHint(h string) *Error { e.Hint = h; return e }

var (
	errProjectRequired = func() *Error {
		return newErr(http.StatusBadRequest, "project_required", "this operation needs a project").
			withHint("set the X-Impamind-Project header, pass project, or call memory_recall with project_hint")
	}
)

// AsError maps any error to a protocol Error.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, auth.ErrNoProject):
		return &Error{Code: "not_found", Status: http.StatusNotFound, Msg: "not found"}
	case errors.Is(err, auth.ErrProjectConflict):
		return &Error{Code: "project_conflict", Status: http.StatusBadRequest, Msg: err.Error()}
	case errors.Is(err, store.ErrClosed):
		return &Error{Code: "session_closed", Status: http.StatusConflict, Msg: "work session is closed", Hint: "call memory_recall to start a new session"}
	case errors.Is(err, store.ErrConflict):
		return &Error{Code: "conflict", Status: http.StatusConflict, Msg: err.Error()}
	case errors.Is(err, store.ErrForbidden), errors.Is(err, auth.ErrForbidden):
		return &Error{Code: "forbidden", Status: http.StatusForbidden, Msg: err.Error()}
	case errors.Is(err, store.ErrInvalid):
		return &Error{Code: "invalid", Status: http.StatusBadRequest, Msg: err.Error()}
	}
	return &Error{Code: "internal", Status: http.StatusInternalServerError, Msg: "internal error"}
}
