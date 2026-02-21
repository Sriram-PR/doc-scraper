package log

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func newDiscardEntry() *logrus.Entry {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return logrus.NewEntry(logger)
}

func TestNewBadgerLogrusAdapter(t *testing.T) {
	adapter := NewBadgerLogrusAdapter(newDiscardEntry())
	assert.NotNil(t, adapter)
}

func TestBadgerLogrusAdapter_Methods(t *testing.T) {
	adapter := NewBadgerLogrusAdapter(newDiscardEntry())

	assert.NotPanics(t, func() { adapter.Errorf("error %s", "test") })
	assert.NotPanics(t, func() { adapter.Warningf("warning %d", 42) })
	assert.NotPanics(t, func() { adapter.Infof("info %v", true) })
	assert.NotPanics(t, func() { adapter.Debugf("debug") })
}
