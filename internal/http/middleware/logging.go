package middleware

import (
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog"
)

func NewLogger(level string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(lvl)
	return logger
}

func Logging(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			logger.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("request_id", r.Header.Get("X-Request-ID")).
				Dur("latency", time.Since(start)).
				Msg("http request")
		})
	}
}
