package main

import (
	"io"
	"log/slog"
	"strings"
)

func newLogger(output io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 {
				switch attr.Key {
				case slog.TimeKey:
					attr.Key = "timestamp"
				case slog.MessageKey:
					attr.Key = "message"
				case slog.LevelKey:
					attr.Value = slog.StringValue(strings.ToLower(attr.Value.String()))
				}
			}
			return attr
		},
	})).With("service.name", "fileserver")
}
