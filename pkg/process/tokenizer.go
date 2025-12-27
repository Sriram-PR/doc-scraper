package process

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var (
	defaultCodec tokenizer.Codec
	codecMu      sync.RWMutex
	initialized  bool
)

// InitTokenizer initializes the tokenizer with the specified encoding.
// Common encodings: "cl100k_base" (GPT-4, Claude), "p50k_base" (GPT-3).
// If encoding is empty, defaults to "cl100k_base".
func InitTokenizer(encoding string) error {
	codecMu.Lock()
	defer codecMu.Unlock()

	if encoding == "" {
		encoding = "cl100k_base"
	}

	var enc tokenizer.Encoding
	switch encoding {
	case "cl100k_base":
		enc = tokenizer.Cl100kBase
	case "p50k_base":
		enc = tokenizer.P50kBase
	case "p50k_edit":
		enc = tokenizer.P50kEdit
	case "r50k_base":
		enc = tokenizer.R50kBase
	case "o200k_base":
		enc = tokenizer.O200kBase
	default:
		enc = tokenizer.Cl100kBase
	}

	codec, err := tokenizer.Get(enc)
	if err != nil {
		return err
	}
	defaultCodec = codec
	initialized = true
	return nil
}

// CountTokens returns the token count for the given text.
// If the tokenizer hasn't been initialized, falls back to a rough estimate (chars/4).
func CountTokens(text string) int {
	codecMu.RLock()
	defer codecMu.RUnlock()

	if !initialized || defaultCodec == nil {
		return estimateTokens(text)
	}

	ids, _, err := defaultCodec.Encode(text)
	if err != nil {
		return estimateTokens(text)
	}
	return len(ids)
}

// estimateTokens provides a rough token estimate based on character count.
// Average is roughly 4 characters per token for English text.
func estimateTokens(text string) int {
	return len(text) / 4
}

// IsInitialized returns whether the tokenizer has been initialized.
func IsInitialized() bool {
	codecMu.RLock()
	defer codecMu.RUnlock()
	return initialized
}
