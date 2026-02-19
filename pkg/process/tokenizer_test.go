package process

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitTokenizer(t *testing.T) {
	// Reset state for testing
	codecMu.Lock()
	defaultCodec = nil
	initialized = false
	codecMu.Unlock()

	err := InitTokenizer("cl100k_base")
	require.NoError(t, err)
	assert.True(t, IsInitialized())
}

func TestInitTokenizer_DefaultEncoding(t *testing.T) {
	codecMu.Lock()
	defaultCodec = nil
	initialized = false
	codecMu.Unlock()

	err := InitTokenizer("")
	require.NoError(t, err)
	assert.True(t, IsInitialized())
}

func TestCountTokens_Initialized(t *testing.T) {
	codecMu.Lock()
	defaultCodec = nil
	initialized = false
	codecMu.Unlock()

	err := InitTokenizer("cl100k_base")
	require.NoError(t, err)

	// Test with known text
	text := "Hello, world!"
	count := CountTokens(text)
	assert.Positive(t, count)
	// "Hello, world!" should be about 3-4 tokens
	assert.LessOrEqual(t, count, 10)
}

func TestCountTokens_Uninitialized(t *testing.T) {
	codecMu.Lock()
	defaultCodec = nil
	initialized = false
	codecMu.Unlock()

	// Without initialization, should fall back to estimate
	text := "Hello, world! This is a test."
	count := CountTokens(text)

	// Should use estimate (len/4)
	expected := len(text) / 4
	assert.Equal(t, expected, count)
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"test", 1},           // 4 chars / 4 = 1
		{"hello world", 2},    // 11 chars / 4 = 2
		{"12345678", 2},       // 8 chars / 4 = 2
		{"1234567890123456", 4}, // 16 chars / 4 = 4
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			result := estimateTokens(tt.text)
			assert.Equal(t, tt.expected, result)
		})
	}
}
