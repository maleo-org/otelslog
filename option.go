package otelslog

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type Option func(h *Handler)

func WithDurationValuer(f DurationValuerFunc) Option {
	return func(h *Handler) {
		h.durationFmt = f
	}
}

func StringDurationValuer(d time.Duration) attribute.Value {
	return attribute.StringValue(d.String())
}

func WithTimeValuer(f TimeValuerFunc) Option {
	return func(h *Handler) {
		h.timeFmt = f
	}
}

func TimeRFC3339Valuer(t time.Time) attribute.Value {
	return attribute.StringValue(t.Format(time.RFC3339))
}

func TimeRFC3339NanoValuer(t time.Time) attribute.Value {
	return attribute.StringValue(t.Format(time.RFC3339Nano))
}

func TimeUnixValuer(t time.Time) attribute.Value {
	return attribute.Int64Value(t.Unix())
}

func TimeUnixNanoValuer(t time.Time) attribute.Value {
	return attribute.Int64Value(t.UnixNano())
}
