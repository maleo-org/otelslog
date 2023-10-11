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

type AttributeValuer interface {
	// AttributeValue extracts an OpenTelemetry attribute value from the implementor.
	//
	// Note that this method only works on types that are not supported by slog.Kind (except Any)
	// since they are always evaluated first and thus not reaching this method.
	//
	// You should implement this method for types that are of kind:
	//  - structs
	//  - maps
	//  - slices
	//  - arrays
	//  - complex
	//  - basically any value that must use slog.Any to be logged.
	//
	// AttributeValuer interface is not compatible with slog.LogValuer interface because that interface always wins,
	// thus you should implement AttributeValuer for the type that slog.LogValuer outputs instead. The rules above
	// applies to slog.LogValuer output value as well.
	AttributeValue() attribute.Value
}

type Handler struct {
	inner               slog.Handler
	durationFmt         DurationValuerFunc
	timeFmt             TimeValuerFunc
	onlyRecordErrorOnce bool
}

type (
	DurationValuerFunc = func(d time.Duration) attribute.Value
	TimeValuerFunc     = func(t time.Time) attribute.Value
)

// New returns a new Handler that wraps the default slog handler.
func New() *Handler {
	return NewWithHandler(slog.Default().Handler())
}

// NewWithHandler returns a new Handler that wraps the given handler.
func NewWithHandler(handler slog.Handler) *Handler {
	// Avoid multiple layers of wrapping with the same handler.
	if h, ok := handler.(*Handler); ok {
		handler = h.Handler()
	}
	return &Handler{
		inner:       handler,
		durationFmt: StringDurationValuer,
		timeFmt:     TimeRFC3339NanoValuer,
	}
}

// Enabled implements the slog.Handler interface.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements the slog.Handler interface.
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
				if attr.Value.Kind() == slog.KindAny {
					value := attr.Value.Any()
					if err, ok := value.(error); ok {
						span.RecordError(err)
						typ := reflect.TypeOf(err).String()
						attrs = append(
							attrs,
							semconv.ExceptionTypeKey.String(typ),
							semconv.ExceptionMessageKey.String(err.Error()),
						)
					}
				}
				return true
			},
		)

		span.AddEvent("log",
			trace.WithAttributes(attrs...),
		)

		if spanCtx := span.SpanContext(); spanCtx.HasTraceID() {
			record.AddAttrs(slog.String(traceKey, spanCtx.TraceID().String()))
		}
	}
	return h.inner.Handle(ctx, record)
}

// WithAttrs implements the slog.Handler interface.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return NewWithHandler(h.inner.WithAttrs(attrs))
}

// WithGroup implements the slog.Handler interface.
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
		return append(kv, attribute.KeyValue{
			Key:   attribute.Key(key),
			Value: h.durationFmt(val.Duration()),
		})
	case slog.KindFloat64:
		return append(kv, attribute.Float64(key, val.Float64()))
	case slog.KindString:
		return append(kv, attribute.String(key, val.String()))
	case slog.KindTime:
		return append(kv, attribute.KeyValue{
			Key:   attribute.Key(key),
			Value: h.timeFmt(val.Time()),
		})
	case slog.KindGroup:
		for _, a := range val.Group() {
			// TODO: Check if group prefix is needed.
			kv = h.appendSlogAttr(kv, a)
		}
		return kv
	case slog.KindAny:
		v := val.Any()
		if v, ok := v.(AttributeValuer); ok {
			return append(kv, attribute.KeyValue{
				Key:   attribute.Key(key),
				Value: v.AttributeValue(),
			})
		}
		return append(kv, otelutil.Attribute(key, v))
	}

	return kv
}
