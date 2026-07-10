package core

import (
	"context"
	"log"

	"github.com/gin-gonic/gin"
)

// ErrorReporter routes plugin errors to the persistent error
// log (the error_logs table behind /admin/errors). Plugins MUST
// call this instead of writing to stderr directly OR calling
// pkg/services.LogServiceError / web/handlers.JSONInternalError
// — those still work but require imports that pkg/core doesn't
// want plugins to take.
//
// The "op" argument is a stable label (e.g. "wiki/index",
// "forum/post-create") used to group occurrences for the admin
// merge view. Don't include user input or row IDs in op — those
// defeat the dedup behaviour.
type ErrorReporter interface {
	// Report logs err to stderr AND persists a row to
	// error_logs. Safe to call with a nil err (no-op).
	Report(ctx context.Context, op string, err error)

	// HandlerError is the gin-aware variant. It calls Report
	// for the persistence side AND writes the standard
	// 500-internal-server-error envelope back to the client.
	// Use from plugin handlers in place of
	// handlers.JSONInternalError.
	HandlerError(c *gin.Context, op string, err error)
}

// ErrorAdapter bundles the function references the host hands to
// NewErrorReporter. ReportFn corresponds to
// services.LogServiceError (or compatible); HandlerErrorFn
// corresponds to handlers.JSONInternalError.
type ErrorAdapter struct {
	ReportFn       func(ctx context.Context, op string, err error)
	HandlerErrorFn func(c *gin.Context, op string, err error)
}

// NewErrorReporter constructs an ErrorReporter from the given
// adapter. Either callback may be nil; in that case the
// implementation logs to stderr only (and, for HandlerError,
// writes a plain 500 to the response).
func NewErrorReporter(a ErrorAdapter) ErrorReporter { return &errorAdapter{a: a} }

type errorAdapter struct{ a ErrorAdapter }

func (e *errorAdapter) Report(ctx context.Context, op string, err error) {
	if err == nil {
		return
	}
	if e.a.ReportFn != nil {
		e.a.ReportFn(ctx, op, err)
		return
	}
	log.Printf("plugin %s failed: %v", op, err)
}

func (e *errorAdapter) HandlerError(c *gin.Context, op string, err error) {
	if err == nil {
		return
	}
	if e.a.HandlerErrorFn != nil {
		e.a.HandlerErrorFn(c, op, err)
		return
	}
	// Fallback: log + 500. Mirrors handlers.JSONInternalError's
	// envelope shape so JS clients don't trip over the change
	// when this code path is the one running.
	log.Printf("plugin %s failed: %v", op, err)
	c.JSON(500, gin.H{"ok": false, "error": "internal server error"})
}
