package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
)

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// redactHandler scrubs secret values from log records an integration writes
// through Deps.Log: the message and every string, error or Stringer attr.
type redactHandler struct {
	next slog.Handler
	r    redactor
}

func newRedactLogger(base *slog.Logger, r redactor) *slog.Logger {
	if len(r.values) == 0 {
		return base
	}
	return slog.New(redactHandler{next: base.Handler(), r: r})
}

func (h redactHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.scrub(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.r.scrub(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]any, len(attrs))
		for i, g := range attrs {
			out[i] = h.attr(g)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, h.r.scrub(x.Error()))
		case interface{ String() string }:
			return slog.String(a.Key, h.r.scrub(x.String()))
		case []byte:
			return slog.String(a.Key, h.r.scrub(string(x)))
		case nil:
			return slog.Attr{Key: a.Key, Value: v}
		default:
			// Maps and structs would be printed whole; print them here instead,
			// scrubbed.
			return slog.String(a.Key, h.r.scrub(fmt.Sprintf("%+v", x)))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.attr(a)
	}
	return redactHandler{next: h.next.WithAttrs(out), r: h.r}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{next: h.next.WithGroup(name), r: h.r}
}
