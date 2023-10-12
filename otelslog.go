package otelslog

import (
	"context"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"time"

	"github.com/uptrace/opentelemetry-go-extra/otelutil"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

const traceKey = "trace_id"

// AttributeValuer extracts an OpenTelemetry attribute value from the implemented type.
//
// AttributeValuer interface is not compatible with [slog.LogValuer] interface because the value
// will be resolved first before being checked for AttributeValuer interface.
//
// Implement AttributeValuer interface for the type that [slog.LogValuer] outputs instead.
type AttributeValuer interface {
	// AttributeValue returns an OpenTelemetry attribute value.
	AttributeValue() attribute.Value
}

type Handler struct {
	logEventName        string
	inner               slog.Handler
	durationFmt         DurationValuerFunc
	timeFmt             TimeValuerFunc
	onlyRecordErrorOnce bool
}

type (
	DurationValuerFunc = func(d time.Duration) attribute.Value
	TimeValuerFunc     = func(t time.Time) attribute.Value
)

// New returns a new [Handler] that wraps the default slog handler.
func New(opts ...Option) *Handler {
	return NewWithHandler(slog.Default().Handler(), opts...)
}

// NewWithHandler returns a new [Handler] that wraps the given handler.
func NewWithHandler(handler slog.Handler, opts ...Option) *Handler {
	// Avoid multiple layers of wrapping with the same handler.
	if h, ok := handler.(*Handler); ok {
		handler = h.Handler()
	}
	h := &Handler{
		logEventName: "log",
		inner:        handler,
		durationFmt: func(d time.Duration) attribute.Value {
			return attribute.StringValue(d.String())
		},
		timeFmt: func(t time.Time) attribute.Value {
			return attribute.StringValue(t.Format(time.RFC3339Nano))
		},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Enabled implements the [slog.Handler] interface.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements the [slog.Handler] interface.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		if record.Level >= slog.LevelError {
			span.SetStatus(codes.Error, record.Message)
		}

		attrs := []attribute.KeyValue{
			attribute.String("log.message", record.Message),
			attribute.String("log.severity", record.Level.String()),
		}

		record.Attrs(
			func(attr slog.Attr) bool {
				attrs = h.appendSlogAttr(attrs, attr)
				if err, ok := attr.Value.Any().(error); ok {
					span.RecordError(err)
					typ := reflect.TypeOf(err).String()
					a := []attribute.KeyValue{
						semconv.ExceptionTypeKey.String(typ),
						semconv.ExceptionMessageKey.String(err.Error()),
					}

					detail := otelutil.Attribute("exception.detail", err)
					if detail.Value.Type() == attribute.STRING {
						out := detail.Value.AsString()
						switch out {
						// If JSON String is empty, we don't want to add it to the attributes, since they bring
						// no value to the user.
						//
						// .Error() message is already added to the attributes above.
						case "{}", "[]", "null", `""`, "":
						default:
							a = append(a, detail)
						}
					}
					attrs = append(
						attrs,
						a...,
					)
				}
				return true
			},
		)

		span.AddEvent(
			h.logEventName,
			trace.WithAttributes(attrs...),
		)

		if spanCtx := span.SpanContext(); spanCtx.HasTraceID() {
			record.AddAttrs(slog.String(traceKey, spanCtx.TraceID().String()))
		}
	}
	return h.inner.Handle(ctx, record)
}

// WithAttrs implements the [slog.Handler] interface.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return NewWithHandler(h.inner.WithAttrs(attrs))
}

// WithGroup implements the [slog.Handler] interface.
func (h *Handler) WithGroup(name string) slog.Handler {
	return NewWithHandler(h.inner.WithGroup(name))
}

// Handler returns the wrapped handler.
func (h *Handler) Handler() slog.Handler {
	return h.inner
}

func (h *Handler) appendSlogAttr(kv []attribute.KeyValue, attr slog.Attr) []attribute.KeyValue {
	if attr.Equal(slog.Attr{}) {
		return kv
	}

	var (
		val = attr.Value.Resolve()
		key = attr.Key
	)

	if av, ok := val.Any().(AttributeValuer); ok {
		return append(
			kv, attribute.KeyValue{
				Key:   attribute.Key(key),
				Value: av.AttributeValue(),
			},
		)
	}

	switch val.Kind() {
	case slog.KindBool:
		return append(kv, attribute.Bool(key, val.Bool()))
	case slog.KindInt64:
		return append(kv, attribute.Int64(key, val.Int64()))
	case slog.KindUint64:
		v := val.Uint64()
		if v > math.MaxInt64 {
			s := strconv.FormatUint(v, 10)
			return append(kv, attribute.String(key, s))
		} else {
			return append(kv, attribute.Int64(key, int64(v)))
		}
	case slog.KindDuration:
		return append(
			kv, attribute.KeyValue{
				Key:   attribute.Key(key),
				Value: h.durationFmt(val.Duration()),
			},
		)
	case slog.KindFloat64:
		return append(kv, attribute.Float64(key, val.Float64()))
	case slog.KindString:
		return append(kv, attribute.String(key, val.String()))
	case slog.KindTime:
		return append(
			kv, attribute.KeyValue{
				Key:   attribute.Key(key),
				Value: h.timeFmt(val.Time()),
			},
		)
	case slog.KindGroup:
		groupPrefix := key + "."
		for _, a := range val.Group() {
			kv = h.appendSlogAttr(
				kv, slog.Attr{
					Key:   groupPrefix + a.Key,
					Value: a.Value,
				},
			)
		}
		return kv
	case slog.KindAny:
		v := val.Any()
		// error is already handled outside.
		if _, ok := v.(error); ok {
			return kv
		}
		return append(kv, otelutil.Attribute(key, v))
	}

	return kv
}
