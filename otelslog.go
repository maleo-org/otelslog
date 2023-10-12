package otelslog

import (
	"context"
	"log/slog"
	"math"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/opentelemetry-go-extra/otelutil"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

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
	logEventName          string
	inner                 slog.Handler
	durationFmt           DurationValuerFunc
	timeFmt               TimeValuerFunc
	onlyRecordErrorOnce   bool
	includeAttributeLevel slog.Leveler
	prefixGroup           string
	prefixAttr            []slog.Attr
	levelFmt              LevelValuerFunc
	traceKey              string
	sourceFmt             SourceKeyValuerFunc
	includeSource         bool
	sourceDepth           int
}

func (h *Handler) clone() *Handler {
	return &Handler{
		logEventName:          h.logEventName,
		inner:                 h.inner,
		durationFmt:           h.durationFmt,
		timeFmt:               h.timeFmt,
		onlyRecordErrorOnce:   h.onlyRecordErrorOnce,
		includeAttributeLevel: h.includeAttributeLevel,
		prefixGroup:           h.prefixGroup,
		prefixAttr:            h.prefixAttr,
		levelFmt:              h.levelFmt,
		traceKey:              h.traceKey,
		sourceFmt:             h.sourceFmt,
		includeSource:         h.includeSource,
		sourceDepth:           h.sourceDepth,
	}
}

type (
	DurationValuerFunc  = func(d time.Duration) attribute.Value
	TimeValuerFunc      = func(t time.Time) attribute.Value
	LevelValuerFunc     = func(l slog.Leveler) attribute.Value
	SourceKeyValuerFunc = func(source *slog.Source) []attribute.KeyValue
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
		includeAttributeLevel: slog.LevelInfo,
		traceKey:              "trace_id",
		levelFmt: func(l slog.Leveler) attribute.Value {
			return attribute.StringValue(l.Level().String())
		},
		sourceFmt: func(source *slog.Source) []attribute.KeyValue {
			fs := strings.Split(source.Function, "/") // Take the package and function name only.
			f := fs[len(fs)-1]
			return []attribute.KeyValue{
				semconv.CodeFunctionKey.String(f),
				semconv.CodeFilepathKey.String(source.File),
				semconv.CodeLineNumberKey.Int(source.Line),
			}
		},
		includeSource: true,
		sourceDepth:   1,
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
		if spanCtx := span.SpanContext(); spanCtx.HasTraceID() {
			// Add first, because trace_id should be available in all cases.
			record.AddAttrs(slog.String(h.traceKey, spanCtx.TraceID().String()))
		}

		if record.Level >= slog.LevelError {
			span.SetStatus(codes.Error, record.Message)
		}

		// Only higher level than includeAttributeLevel will be added to span.
		//
		// Error record will be added to span regardless of the level.
		if record.Level < h.includeAttributeLevel.Level() {
			record.Attrs(
				func(attr slog.Attr) bool {
					if attr.Key == h.traceKey { // skip trace_id attribute
						return true
					}
					if err, ok := attr.Value.Any().(error); ok {
						span.RecordError(err)
					}
					return true
				},
			)
			return h.inner.Handle(ctx, record)
		}

		lt := record.Time.Round(0)
		attrs := []attribute.KeyValue{
			{"log.time", h.timeFmt(lt)},
			attribute.String("log.message", record.Message),
			attribute.String("log.severity", record.Level.String()),
		}

		for _, attr := range h.prefixAttr {
			attrs = h.appendSlogAttr(attrs, attr)
		}

		if h.includeSource {
			fs := runtime.CallersFrames([]uintptr{record.PC})
			var f runtime.Frame
			for i := h.sourceDepth; i > 0; i-- {
				next, _ := fs.Next()
				if next.File != "" {
					f = next
				}
			}
			if f.File != "" {
				attrs = append(
					attrs,
					h.sourceFmt(
						&slog.Source{
							Function: f.Function,
							File:     f.File,
							Line:     f.Line,
						},
					)...,
				)
			}
		}

		record.Attrs(
			func(attr slog.Attr) bool {
				if attr.Key == h.traceKey { // skip trace_id attribute
					return true
				}
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
						// Also,.Error() message is already added to the attributes semconv.ExceptionMessageKey.
						// We don't want to add it twice.
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
			h.prefixGroup+h.logEventName,
			trace.WithAttributes(attrs...),
		)
	}
	return h.inner.Handle(ctx, record)
}

// WithAttrs implements the [slog.Handler] interface.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	h2 := h.clone()
	h2.prefixAttr = append(h2.prefixAttr, attrs...)
	h2.inner = h.inner.WithAttrs(attrs)
	return h2
}

// WithGroup implements the [slog.Handler] interface.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := h.clone()
	h2.prefixGroup += name + "."
	h2.inner = h.inner.WithGroup(name)
	return h2
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
