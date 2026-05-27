package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/rs/zerolog"
)

// Recover catches panics in downstream handlers and returns a 500
// error so a single bad request doesn't kill streaming responses on
// other in-flight connections. Without this, http.Server's default
// recover only logs to stderr and abruptly closes the client socket
// — which Claude Code observes as "stream stopped mid-tool-use."
//
// Stack traces are logged at Error level with the request id so the
// origin handler can be identified.
func Recover(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rv := recover()
				if rv == nil {
					return
				}
				if rv == http.ErrAbortHandler {
					panic(rv)
				}
				logger.Error().
					Interface("panic", rv).
					Str("path", r.URL.Path).
					Str("method", r.Method).
					Bytes("stack", debug.Stack()).
					Msg("panic recovered in handler")
				if rec, ok := w.(*statusRecorder); ok && rec.status != http.StatusOK {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal_error","message":"internal_error"}`))
			}()
			next.ServeHTTP(w, r)
		})
	}
}
