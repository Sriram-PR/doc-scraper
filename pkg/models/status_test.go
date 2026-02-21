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

func TestPageStatus_IsValid(t *testing.T) {
	tests := []struct {
		status PageStatus
		want   bool
	}{
		{PageStatusPending, true},
		{PageStatusSuccess, true},
		{PageStatusFailure, true},
		{PageStatusUnset, false},
		{PageStatusNotFound, false},
		{PageStatusDBError, false},
		{PageStatus("arbitrary"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.status.IsValid(), "PageStatus(%q).IsValid()", string(tt.status))
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

func TestImageStatus_IsValid(t *testing.T) {
	tests := []struct {
		status ImageStatus
		want   bool
	}{
		{ImageStatusPending, true},
		{ImageStatusSuccess, true},
		{ImageStatusFailure, true},
		{ImageStatusSkipped, true},
		{ImageStatusUnset, false},
		{ImageStatusNotFound, false},
		{ImageStatusDBError, false},
		{ImageStatus("arbitrary"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.status.IsValid(), "ImageStatus(%q).IsValid()", string(tt.status))
	}
}
