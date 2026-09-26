// Package apperrors is the single error mechanism in Chonkboard. Every layer
// returns *AppError; the HTTP edge maps one to a status and a message. There is
// deliberately no per-package error-to-status switch — that pattern drifts, and
// a drifted mapping is how an internal failure ends up rendered as a 200.
package apperrors

import (
	"errors"
	"fmt"
	"net/http"
)

// Code classifies a failure. It is what Is() compares, so a wrapped copy of a
// sentinel still matches the sentinel.
type Code string

const (
	CodeInvalid      Code = "invalid"
	CodeValidation   Code = "validation"
	CodeUnauthorized Code = "unauthorized"
	CodeForbidden    Code = "forbidden"
	CodeNotFound     Code = "not_found"
	CodeConflict     Code = "conflict"
	CodeRateLimited  Code = "rate_limited"
	CodeTooLarge     Code = "too_large"
	CodeInternal     Code = "internal"
)

// FieldError attaches a message to one form field so a handler can re-render an
// input with its own error without inspecting the cause.
type FieldError struct {
	Field   string
	Message string
}

// AppError is the one error type crossing a layer boundary.
type AppError struct {
	Code    Code
	Message string       // safe to show a user
	Fields  []FieldError // per-field detail, for forms
	Err     error        // the cause; never shown to a user
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Err }

// Is matches on Code alone. This is what lets a handler compare against an
// exported sentinel after the error has been wrapped with a cause on the way up.
func (e *AppError) Is(target error) bool {
	var t *AppError
	if !errors.As(target, &t) {
		return false
	}
	return t.Code == e.Code
}

// WithField returns a copy carrying one more field error.
func (e *AppError) WithField(field, message string) *AppError {
	c := *e
	c.Fields = append(append([]FieldError(nil), e.Fields...), FieldError{field, message})
	return &c
}

// Wrap returns a copy carrying a cause. The user-facing message is unchanged.
func (e *AppError) Wrap(err error) *AppError {
	c := *e
	c.Err = err
	return &c
}

// Status is the HTTP status for a code. Defined once, here.
func (e *AppError) Status() int {
	switch e.Code {
	case CodeInvalid, CodeValidation:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

func New(code Code, message string) *AppError { return &AppError{Code: code, Message: message} }

func Invalid(message string) *AppError      { return New(CodeInvalid, message) }
func Validation(message string) *AppError   { return New(CodeValidation, message) }
func Unauthorized(message string) *AppError { return New(CodeUnauthorized, message) }
func Forbidden(message string) *AppError    { return New(CodeForbidden, message) }
func NotFound(message string) *AppError     { return New(CodeNotFound, message) }
func Conflict(message string) *AppError     { return New(CodeConflict, message) }
func RateLimited(message string) *AppError  { return New(CodeRateLimited, message) }
func TooLarge(message string) *AppError     { return New(CodeTooLarge, message) }

// Internal always carries the cause and never a specific message: a 5xx tells
// the user nothing useful and must not leak the reason.
func Internal(err error, context string) *AppError {
	return &AppError{
		Code:    CodeInternal,
		Message: "Something went wrong on our end.",
		Err:     fmt.Errorf("%s: %w", context, err),
	}
}

// From normalises any error into an *AppError so the edge has exactly one shape
// to handle. An error that is not ours is an internal failure by definition.
func From(err error) *AppError {
	if err == nil {
		return nil
	}
	var app *AppError
	if errors.As(err, &app) {
		return app
	}
	return Internal(err, "unclassified")
}
