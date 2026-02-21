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

	// Without initialization, should return -1 (not available)
	text := "Hello, world! This is a test."
	count := CountTokens(text)
	assert.Equal(t, -1, count)
}
