package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPageStatus_String(t *testing.T) {
	tests := []struct {
		status PageStatus
		want   string
	}{
		{PageStatusUnset, "unset"},
		{PageStatusPending, "pending"},
		{PageStatusSuccess, "success"},
		{PageStatusFailure, "failure"},
		{PageStatusNotFound, "not_found"},
		{PageStatusDBError, "db_error"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.status.String())
	}
}

func TestImageStatus_String(t *testing.T) {
	tests := []struct {
		status ImageStatus
		want   string
	}{
		{ImageStatusUnset, "unset"},
		{ImageStatusPending, "pending"},
		{ImageStatusSuccess, "success"},
		{ImageStatusFailure, "failure"},
		{ImageStatusSkipped, "skipped"},
		{ImageStatusNotFound, "not_found"},
		{ImageStatusDBError, "db_error"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.status.String())
	}
}

