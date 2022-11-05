package logger

import (
	"github.com/sirupsen/logrus"
)

//go:generate go-enum -f=$GOFILE --flag --names

func New(level Level) *logrus.Logger {
	logger := logrus.New()
	logger.SetLevel(logrus.Level(level))
	logger.Debugf("log level set to %s", logrus.Level(level))
	return logger
}

// Level is a logrus Level
// ENUM(info, panic, fatal, error, warning, debug, trace)
type Level logrus.Level
