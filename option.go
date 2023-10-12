package otelslog

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type Option func(h *Handler)

// WithDurationValuer sets how the duration is formatted when being set as span attribute.
//
// Use this option if you want to use a custom duration format and
// none of the provided duration valuer suits your needs.
func WithDurationValuer(f DurationValuerFunc) Option {
	return func(h *Handler) {
		h.durationFmt = f
	}
}

// WithDurationSecondsValuer sets duration types to be formatted as seconds when being set as span's log event
// attribute.
func WithDurationSecondsValuer() Option {
	return func(h *Handler) {
		h.durationFmt = func(d time.Duration) attribute.Value {
			return attribute.Float64Value(d.Seconds())
		}
	}
}

// WithStringDurationValuer sets duration types to be formatted as string when being set as span's log event attribute.
func WithStringDurationValuer() Option {
	return func(h *Handler) {
		h.durationFmt = func(d time.Duration) attribute.Value {
			return attribute.StringValue(d.String())
		}
	}
}

// WithTimeValuer sets how the time is formatted when being set as span's log event attribute.
//
// Use this option if you want to use a custom time format and none of the provided time formatters suits your needs.
func WithTimeValuer(f TimeValuerFunc) Option {
	return func(h *Handler) {
		h.timeFmt = f
	}
}

// WithTimeRFC3339Valuer sets time types to be formatted as RFC3339 when being set as span attribute.
func WithTimeRFC3339Valuer() Option {
	return func(h *Handler) {
		h.timeFmt = func(t time.Time) attribute.Value {
			return attribute.StringValue(t.Format(time.RFC3339))
		}
	}
}

// WithTimeRFC3339NanoValuer sets time types to be formatted as RFC3339Nano when being set as span attribute.
func WithTimeRFC3339NanoValuer() Option {
	return func(h *Handler) {
		h.timeFmt = func(t time.Time) attribute.Value {
			return attribute.StringValue(t.Format(time.RFC3339Nano))
		}
	}
}

// WithTimeUnixValuer sets time types to be formatted as Unix Seconds when being set as span attribute.
func WithTimeUnixValuer() Option {
	return func(h *Handler) {
		h.timeFmt = func(t time.Time) attribute.Value {
			return attribute.Int64Value(t.Unix())
		}
	}
}

// WithTimeUnixNanoValuer sets time types to be formatted as Unix Nano Seconds when being set as span attribute.
func WithTimeUnixNanoValuer() Option {
	return func(h *Handler) {
		h.timeFmt = func(t time.Time) attribute.Value {
			return attribute.Int64Value(t.UnixNano())
		}
	}
}

// WithLogEventName sets the name of the log event attribute. Default is "log".
func WithLogEventName(name string) Option {
	return func(h *Handler) {
		h.logEventName = name
	}
}
