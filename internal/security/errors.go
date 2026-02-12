package security

import (
	"errors"
	"os"
	"strings"
)

// ClassifiedError separates a user-safe message from verbose debug details.
type ClassifiedError struct {
	UserSafe    string
	DebugDetail string
}

func (e *ClassifiedError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.UserSafe) == "" {
		return "operation failed"
	}
	return e.UserSafe
}

// NewClassifiedError creates a new error with separated user-safe and debug details.
func NewClassifiedError(userSafe, debugDetail string) error {
	return &ClassifiedError{UserSafe: userSafe, DebugDetail: debugDetail}
}

// UserMessage returns a message safe to show in CLI/TUI contexts.
func UserMessage(err error, redact bool) string {
	if err == nil {
		return ""
	}
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		msg := ce.UserSafe
		if msg == "" {
			msg = "operation failed"
		}
		if redact {
			return RedactMessage(msg)
		}
		return msg
	}
	if redact {
		return RedactMessage(err.Error())
	}
	return err.Error()
}

// DebugMessage returns detailed error text for logs.
func DebugMessage(err error) string {
	if err == nil {
		return ""
	}
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		if strings.TrimSpace(ce.DebugDetail) != "" {
			return ce.DebugDetail
		}
	}
	return err.Error()
}

// RedactMessage strips common sensitive path prefixes from user-visible text.
func RedactMessage(msg string) string {
	if msg == "" {
		return msg
	}
	out := msg
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = strings.ReplaceAll(out, home, "~")
	}
	if idx := strings.Index(out, "/.ssh/"); idx >= 0 {
		out = strings.ReplaceAll(out, "/.ssh/", "/.ssh/[redacted]/")
	}
	return out
}
