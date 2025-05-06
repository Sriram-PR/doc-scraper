package log

import "github.com/sirupsen/logrus"

// BadgerLogrusAdapter implements badger.Logger interface using logrus
type BadgerLogrusAdapter struct {
	*logrus.Entry // Embed logrus Entry
}

// NewBadgerLogrusAdapter creates a new adapter
func NewBadgerLogrusAdapter(entry *logrus.Entry) *BadgerLogrusAdapter {
	return &BadgerLogrusAdapter{entry}
}

// Errorf logs an error message
func (l *BadgerLogrusAdapter) Errorf(f string, v ...interface{}) { l.Entry.Errorf(f, v...) }

// Warningf logs a warning message
func (l *BadgerLogrusAdapter) Warningf(f string, v ...interface{}) { l.Entry.Warningf(f, v...) }

// Infof logs an info message
func (l *BadgerLogrusAdapter) Infof(f string, v ...interface{}) { l.Entry.Infof(f, v...) }

// Debugf logs a debug message
func (l *BadgerLogrusAdapter) Debugf(f string, v ...interface{}) { l.Entry.Debugf(f, v...) }
