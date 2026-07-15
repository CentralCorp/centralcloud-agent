package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

var secretPattern = regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|authorization|database_url)([=:]\s*)([^\s,;]+)`)

func Redact(s string) string { return secretPattern.ReplaceAllString(s, `$1$2[REDACTED]`) }

type redactingHandler struct{ next slog.Handler }

func (h redactingHandler) Enabled(c context.Context, l slog.Level) bool { return h.next.Enabled(c, l) }
func (h redactingHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return redactingHandler{h.next.WithAttrs(redactAttrs(a))}
}
func (h redactingHandler) WithGroup(n string) slog.Handler {
	return redactingHandler{h.next.WithGroup(n)}
}
func (h redactingHandler) Handle(c context.Context, r slog.Record) error {
	r.Message = Redact(r.Message)
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool { out.AddAttrs(redactAttr(a)); return true })
	return h.next.Handle(c, out)
}
func redactAttrs(in []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(in))
	for i, a := range in {
		out[i] = redactAttr(a)
	}
	return out
}
func redactAttr(a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	if strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "token") || k == "authorization" || k == "database_url" {
		return slog.String(a.Key, "[REDACTED]")
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, Redact(a.Value.String()))
	}
	return a
}
func New(level slog.Level) *slog.Logger {
	return slog.New(redactingHandler{slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})})
}

func ScrubJSON(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return []byte(Redact(string(b)))
	}
	scrub(&v)
	out, _ := json.Marshal(v)
	return out
}
func scrub(v *any) {
	switch x := (*v).(type) {
	case map[string]any:
		for k, val := range x {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "password") || strings.Contains(lk, "secret") || strings.Contains(lk, "token") || lk == "authorization" {
				x[k] = "[REDACTED]"
			} else {
				scrub(&val)
				x[k] = val
			}
		}
	case []any:
		for i := range x {
			scrub(&x[i])
		}
	case string:
		*v = Redact(x)
	}
}
