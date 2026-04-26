package middleware

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

const (
	colorReset  = "\x1b[0m"
	colorDim    = "\x1b[2m"
	colorRed    = "\x1b[31m"
	colorGreen  = "\x1b[32m"
	colorYellow = "\x1b[33m"
	colorBlue   = "\x1b[34m"
	colorPurple = "\x1b[35m"
	colorCyan   = "\x1b[36m"
	colorGray   = "\x1b[90m"
)

func NewLogger(level string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	writer := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05", NoColor: false}
	writer.FormatLevel = func(i interface{}) string {
		level := strings.ToUpper(fmt.Sprint(i))
		return fmt.Sprintf("%s%s%s", levelColor(level), levelBadge(level), colorReset)
	}
	writer.FormatMessage = func(i interface{}) string {
		return fmt.Sprintf("%s", i)
	}
	writer.FormatFieldName = func(i interface{}) string {
		return fmt.Sprintf("%s%s=%s", colorDim, i, colorReset)
	}
	writer.FormatFieldValue = func(i interface{}) string {
		return fmt.Sprintf("%s%v%s", colorCyan, i, colorReset)
	}

	logger := zerolog.New(writer).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(lvl)
	return logger
}

func Logging(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rw, r)

			latency := time.Since(start)
			line := fmt.Sprintf(
				"%-28s %s %s",
				formatRoute(r.Method, r.URL.RequestURI()),
				formatStatus(rw.status),
				formatLatency(latency),
			)

			event := logger.Info()
			if rw.status >= http.StatusInternalServerError {
				event = logger.Error()
			} else if rw.status >= http.StatusBadRequest {
				event = logger.Warn()
			}
			event.Msg(line)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func levelBadge(level string) string {
	switch level {
	case "DEBUG":
		return "> dbg"
	case "INFO":
		return "+ ok "
	case "WARN":
		return "! wrn"
	case "ERROR":
		return "x err"
	case "FATAL", "PANIC":
		return "# die"
	default:
		return "- log"
	}
}

func levelColor(level string) string {
	switch level {
	case "DEBUG":
		return colorPurple
	case "INFO":
		return colorGreen
	case "WARN":
		return colorYellow
	case "ERROR", "FATAL", "PANIC":
		return colorRed
	default:
		return colorReset
	}
}

func formatRoute(method, path string) string {
	plain := fmt.Sprintf("%-6s %s", method, shortPath(path))
	if len(plain) > 28 {
		plain = plain[:25] + "..."
	}
	return methodColor(method) + fmt.Sprintf("%-28s", plain) + colorReset
}

func methodColor(method string) string {
	color := map[string]string{
		http.MethodGet:     colorGreen,
		http.MethodPost:    colorBlue,
		http.MethodPut:     colorYellow,
		http.MethodPatch:   colorPurple,
		http.MethodDelete:  colorRed,
		http.MethodOptions: colorGray,
	}[method]
	if color == "" {
		color = colorCyan
	}
	return color
}

func formatStatus(status int) string {
	color := colorGreen
	if status >= http.StatusInternalServerError {
		color = colorRed
	} else if status >= http.StatusBadRequest {
		color = colorYellow
	} else if status >= http.StatusMultipleChoices {
		color = colorCyan
	}
	return fmt.Sprintf("%s%d%s", color, status, colorReset)
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%s%4dµs%s", colorGray, d.Microseconds(), colorReset)
	}
	if d < time.Second {
		return fmt.Sprintf("%s%4dms%s", colorGray, d.Milliseconds(), colorReset)
	}
	return fmt.Sprintf("%s%5.1fs%s", colorGray, d.Seconds(), colorReset)
}

func shortPath(path string) string {
	if len(path) <= 80 {
		return path
	}
	return path[:77] + "..."
}
