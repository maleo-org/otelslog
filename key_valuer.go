package otelslog

import "go.opentelemetry.io/otel/attribute"

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
	AttributeKeyValue(ctx AKVContext) []attribute.KeyValue
}

type AKVContext struct {
	// Key is the log field key the user used for the value.
	Key string
	// HandlerGroup is the list of group set by [slog.Logger.WithGroup]. handlerGroup is nil if the key is not
	// under any handler group.
	HandlerGroup []string
	// ElementGroup is the list of group names that the key is under.
	// ElementGroup does not include group set by [slog.Logger.WithGroup].
	//
	// `elementGroup` is nil if the key is not
	// under any group.
	ElementGroup []string
}
