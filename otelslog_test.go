package otelslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type HandlerAssertFunc = func(ctx context.Context, record slog.Record, h *TestingHandler)

type TestingHandler struct {
	T       *testing.T
	asserts []HandlerAssertFunc
}

func (t TestingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (t TestingHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, assert := range t.asserts {
		assert(ctx, record, &t)
	}
	return nil
}

func (t TestingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return t
}

func (t TestingHandler) WithGroup(name string) slog.Handler {
	return t
}

type errorAttributeValuer struct {
	Message string
}

func (e errorAttributeValuer) AttributeValue() attribute.Value {
	b, _ := json.Marshal(e)
	return attribute.StringValue(string(b))
}

func (e errorAttributeValuer) Error() string {
	return e.Message
}

type ExporterAssertFunc = func(t *testing.T, buf *bytes.Buffer)

type exportedData struct {
	Name        string `json:"Name"`
	SpanContext struct {
		TraceID    string `json:"TraceID"`
		SpanID     string `json:"SpanID"`
		TraceFlags string `json:"TraceFlags"`
		TraceState string `json:"TraceState"`
		Remote     bool   `json:"Remote"`
	} `json:"SpanContext"`
	Parent struct {
		TraceID    string `json:"TraceID"`
		SpanID     string `json:"SpanID"`
		TraceFlags string `json:"TraceFlags"`
		TraceState string `json:"TraceState"`
		Remote     bool   `json:"Remote"`
	} `json:"Parent"`
	SpanKind   int       `json:"SpanKind"`
	StartTime  time.Time `json:"StartTime"`
	EndTime    time.Time `json:"EndTime"`
	Attributes any       `json:"Attributes"`
	Events     []struct {
		Name       string `json:"Name"`
		Attributes []struct {
			Key   string `json:"Key"`
			Value struct {
				Type  string `json:"Type"`
				Value any    `json:"Value"`
			} `json:"Value"`
		} `json:"Attributes"`
		DroppedAttributeCount int       `json:"DroppedAttributeCount"`
		Time                  time.Time `json:"Time"`
	} `json:"Events"`
	Links  any `json:"Links"`
	Status struct {
		Code        string `json:"Code"`
		Description string `json:"Description"`
	} `json:"Status"`
	DroppedAttributes int `json:"DroppedAttributes"`
	DroppedEvents     int `json:"DroppedEvents"`
	DroppedLinks      int `json:"DroppedLinks"`
	ChildSpanCount    int `json:"ChildSpanCount"`
	Resource          []struct {
		Key   string `json:"Key"`
		Value struct {
			Type  string `json:"Type"`
			Value string `json:"Value"`
		} `json:"Value"`
	} `json:"Resource"`
	InstrumentationLibrary struct {
		Name      string `json:"Name"`
		Version   string `json:"Version"`
		SchemaURL string `json:"SchemaURL"`
	} `json:"InstrumentationLibrary"`
}

func TestHandler(t *testing.T) {
	type input struct {
		message        string
		level          slog.Level
		fields         []any
		prefixFields   []any
		group          string
		handlerOptions []Option
	}
	tests := []struct {
		name           string
		input          input
		handlerAsserts []HandlerAssertFunc
		exportAssert   ExporterAssertFunc
	}{
		{
			name: "should give correct output",
			input: input{
				message: "test",
				level:   slog.LevelInfo,
				fields: []any{
					"key", "value",
					"foo", 12345,
					"big_number", uint64(18446744073709551615),
					"err", errors.New("test error"),
					"some_object", map[string]any{"foo": "bar", "baz": 123},
					slog.Any("err2", errors.New("test error 2")),
					"err3", errorAttributeValuer{Message: "test error 3"},
					slog.Group(
						"group",
						slog.String("nnn", "vvv"),
					),
					"dur", 3 * time.Second,
					"time2", time.Date(2021, 1, 1, 0, 0, 0, 50, time.UTC),
					"bool", true,
					"float", 1.2345,
				},
				prefixFields: []any{"prefix", true},
			},
			exportAssert: func(t *testing.T, buf *bytes.Buffer) {
				export := exportedData{}
				debug := buf.String()
				err := json.NewDecoder(buf).Decode(&export)
				if !assert.NoError(t, err, "should not error when decoding json") {
					return
				}

				var (
					logFound        bool
					attrFound       bool
					exceptionFound  bool
					errLogFound     bool
					logMessageFound bool
					logLevelFound   bool
					exceptionCount  int
					groupFound      bool
					prefixFound     bool
				)

				for _, event := range export.Events {
					if event.Name == "exception" {
						exceptionFound = true
						exceptionCount++
					}
					if event.Name == "log" {
						logFound = true
						for _, attr := range event.Attributes {
							if attr.Key == "prefix" {
								prefixFound = true
								assert.Equal(t, true, attr.Value.Value)
							}
							if attr.Key == "key" {
								attrFound = true
								assert.Equal(t, "value", attr.Value.Value)
							}
							if attr.Key == "err" {
								errLogFound = true
								assert.Equal(t, "test error", attr.Value.Value)
							}
							if attr.Key == "log.message" {
								logMessageFound = true
								assert.Equal(t, "test", attr.Value.Value)
							}
							if attr.Key == "log.severity" {
								logLevelFound = true
								assert.Equal(t, "INFO", attr.Value.Value)
							}
							if attr.Key == "err3" {
								assert.Equal(t, "{\"Message\":\"test error 3\"}", attr.Value.Value)
							}
							if attr.Key == "group.nnn" {
								groupFound = true
								assert.Equal(t, "vvv", attr.Value.Value)
							}
							if attr.Key == "foo" {
								assert.Equal(
									t, float64(12345),
									attr.Value.Value,
								) // json numbers are always float64 when using any
							}
							if attr.Key == "big_number" {
								assert.Equal(
									t, "18446744073709551615",
									attr.Value.Value,
								)
							}
							if attr.Key == "dur" {
								assert.Equal(
									t, "3s",
									attr.Value.Value,
								)
							}
							if attr.Key == "time2" {
								assert.Equal(
									t, "2021-01-01T00:00:00.00000005Z",
									attr.Value.Value,
								)
							}
						}
					}
				}
				assert.True(t, logFound, "log event should be found")
				assert.True(t, attrFound, "attribute key should be found")
				assert.True(t, exceptionFound, "exception event should be found")
				assert.True(t, errLogFound, "error should be found in log event")
				assert.True(t, logMessageFound, "log message should be found")
				assert.True(t, logLevelFound, "log level should be found")
				assert.Equal(t, 3, exceptionCount, "should have 3 exceptions")
				assert.True(t, groupFound, "group should be found")
				assert.True(t, prefixFound, "prefix should be found")

				if t.Failed() {
					t.Log(debug)
				}
			},
			handlerAsserts: []HandlerAssertFunc{
				func(ctx context.Context, record slog.Record, h *TestingHandler) {
					span := trace.SpanFromContext(ctx)
					if !assert.True(h.T, span.IsRecording(), "span should be recording") {
						return
					}
					var found bool
					record.Attrs(
						func(attr slog.Attr) bool {
							if attr.Key == "key" {
								assert.Equal(h.T, "value", attr.Value.Resolve().String())
							}
							if attr.Key == "trace_id" {
								found = true
								assert.NotEmptyf(h.T, attr.Value.Resolve().String(), "trace id should not be empty")
							}
							return true
						},
					)
					assert.True(h.T, found, "trace id should be found")
				},
			},
		},
		{
			name: "group should add prefix to event",
			input: input{
				message:        "foo",
				level:          slog.LevelError,
				fields:         []any{"key", "value"},
				prefixFields:   nil,
				group:          "group",
				handlerOptions: []Option{WithStringDurationValuer(), WithTimeRFC3339Valuer()},
			},
			handlerAsserts: nil,
			exportAssert: func(t *testing.T, buf *bytes.Buffer) {
				debug := buf.String()
				export := exportedData{}
				err := json.NewDecoder(buf).Decode(&export)
				if !assert.NoError(t, err, "should not error when decoding json") {
					return
				}

				var found bool

				for _, event := range export.Events {
					if event.Name == "group.log" {
						found = true
						for _, attr := range event.Attributes {
							if attr.Key == "key" {
								assert.Equal(t, "value", attr.Value.Value)
							}
						}
					}
				}
				assert.True(t, found, "group.log event should be found")

				if t.Failed() {
					t.Log(debug)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(
			test.name, func(t *testing.T) {
				buf := &bytes.Buffer{}
				output, err := stdouttrace.New(stdouttrace.WithWriter(buf), stdouttrace.WithPrettyPrint())
				if !assert.NoError(t, err, "should not error when setting up otel") {
					return
				}
				tracerProvider := sdktrace.
					NewTracerProvider(sdktrace.WithBatcher(output), sdktrace.WithResource(resource.Default()))
				defer tracerProvider.Shutdown(context.Background())
				tracer := tracerProvider.Tracer("test")
				ctx, span := tracer.Start(context.Background(), "test")
				handler := NewWithHandler(
					TestingHandler{
						T:       t,
						asserts: test.handlerAsserts,
					},
					test.input.handlerOptions...,
				)
				s := slog.New(handler).WithGroup(test.input.group).With(test.input.prefixFields...)
				s.Log(ctx, test.input.level, test.input.message, test.input.fields...)
				span.End()
				err = tracerProvider.ForceFlush(context.Background())
				if !assert.NoError(t, err, "should not error when flushing traces") {
					return
				}
				test.exportAssert(t, buf)
			},
		)
	}
}
