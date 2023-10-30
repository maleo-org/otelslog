package otelslog

import (
	"context"
	"log/slog"
	"math"
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

type AttributeKeyValuer interface {
	// AttributeKeyValue returns an OpenTelemetry attribute key and value.
	//
	// `key` is the log field key the user used for the value.
	//
	// `handlerGroup` is the list of group set by [slog.Logger.WithGroup]. handlerGroup is nil if the key is not
	// under any handler group.
	//
	// `elementGroup` is the list of group names that the key is under. `elementGroup` is nil if the key is not
	// under any group.
	AttributeKeyValue(handlerGroup []string, elementGroup []string, key string) []attribute.KeyValue
}

type Handler struct {
	inner                 slog.Handler
	includeAttributeLevel slog.Leveler
	levelFmt              LevelValuerFunc
	durationFmt           DurationValuerFunc
	timeFmt               TimeValuerFunc
	sourceFmt             SourceKeyValuerFunc
	group                 []string
	groupDelimiter        string
	logEventName          string
	traceKey              string
	prefixAttr            []slog.Attr
	sourceDepth           int
	onlyRecordErrorOnce   bool
	includeSource         bool
}

func (h *Handler) clone() *Handler {
	return &Handler{
		logEventName:          h.logEventName,
		inner:                 h.inner,
		durationFmt:           h.durationFmt,
		timeFmt:               h.timeFmt,
		onlyRecordErrorOnce:   h.onlyRecordErrorOnce,
		includeAttributeLevel: h.includeAttributeLevel,
		group:                 h.group,
		groupDelimiter:        h.groupDelimiter,
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
		includeSource:  true,
		sourceDepth:    1,
		groupDelimiter: ".",
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
			{Key: "log.time", Value: h.timeFmt(lt)},
			attribute.String("log.message", record.Message),
			attribute.String("log.severity", record.Level.String()),
		}

		for _, attr := range h.prefixAttr {
			attrs = h.appendSlogAttr(attrs, nil, attr)
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
				attrs = h.appendSlogAttr(attrs, nil, attr)
				if err, ok := attr.Value.Any().(error); ok {
					span.RecordError(err)
					// Skip if error and implements AttributeKeyValuer,
					// it is already handled by appendSlogAttr.
					if _, ok := err.(AttributeKeyValuer); ok {
						return true
					}
					detail := otelutil.Attribute(attr.Key, err)
					if detail.Value.Type() == attribute.STRING {
						out := detail.Value.AsString()
						switch out {
						// If JSON String is empty, we will send err.Error() instead to preserve information.
						case "{}", "[]", "null", `""`, "":
							attrs = append(attrs, attribute.String(attr.Key, err.Error()))
						default:
							attrs = append(attrs, detail)
						}
					}
				}
				return true
			},
		)

		eventName := strings.Join(append(h.group, h.logEventName), h.groupDelimiter)

		span.AddEvent(eventName, trace.WithAttributes(attrs...))
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
//
// WithGroup adds prefix to the event name, NOT the log elements themselves when they are added to
// the span.
//
// Group attribute for log elements will be added to the span as is.
//
// Does not effect how the wrapped handler handles the log elements.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := h.clone()
	h2.group = append(h2.group, name)
	h2.inner = h.inner.WithGroup(name)
	return h2
}

// Handler returns the wrapped handler.
func (h *Handler) Handler() slog.Handler {
	return h.inner
}

func (h *Handler) appendSlogAttr(kv []attribute.KeyValue, group []string, attr slog.Attr) []attribute.KeyValue {
	if attr.Equal(slog.Attr{}) {
		return kv
	}

	var (
		val = attr.Value.Resolve()
		key = strings.Join(append(group, attr.Key), h.groupDelimiter)
	)

	if akv, ok := val.Any().(AttributeKeyValuer); ok {
		return append(kv, akv.AttributeKeyValue(h.group, group, attr.Key)...)
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
		g := append(group, attr.Key)
		for _, a := range val.Group() {
			kv = h.appendSlogAttr(kv, g, a)
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
