package logger

import (
	"io"
	"log/slog"
	"os"
)

var L *slog.Logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelInfo,
}))

func Init() {
	L = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: levelFromEnv(),
	}))
}

func levelFromEnv() slog.Level {
	if os.Getenv("DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func SetOutput(w io.Writer) {
	L = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func SetOutputWithLevel(w io.Writer, level slog.Level) {
	L = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
	}))
}
