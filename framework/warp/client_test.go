package warp

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Warp speaks OpenAI to Bifrost's compatibility mount whatever provider is
// configured. Declaring the configured provider instead makes that provider's
// implementation build its own path - Anthropic asks for /v1/messages, which
// under /openai is not a route and comes back as "Method Not Allowed".
func TestWarpAccountSpeaksOpenAIRegardlessOfConfiguredProvider(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Bedrock, schemas.Vertex, schemas.OpenAI} {
		account := &warpAccount{config: &schemas.WarpConfig{Provider: provider, Model: "some-model"}}
		declared, err := account.GetConfiguredProviders()
		require.NoError(t, err)
		require.Equal(t, []schemas.ModelProvider{schemas.OpenAI}, declared,
			"%s must still be reached over the OpenAI wire format", provider)
	}
}

// The configured provider is not lost, it moves into the model string - which is
// what actually routes the request once it reaches Bifrost.
func TestWarpModelCarriesConfiguredProvider(t *testing.T) {
	require.Equal(t, "anthropic/claude-sonnet-5",
		modelForRequest(&schemas.WarpConfig{Provider: schemas.Anthropic, Model: "claude-sonnet-5"}))
	// An operator who typed the qualified form gets exactly what they typed.
	require.Equal(t, "vertex/gemini-2.5-pro",
		modelForRequest(&schemas.WarpConfig{Provider: schemas.Anthropic, Model: "vertex/gemini-2.5-pro"}))
}

// bifrost.Init stores a context derived from the one it is handed, so passing
// the request context ties the cached instance's lifetime to whichever request
// happened to build it. That request ending - a user closing the tab mid-answer
// - then poisons the shared instance for everyone after them.
func TestWarpClientInstanceOutlivesTheRequestThatBuiltIt(t *testing.T) {
	client := NewClient(bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(client.Shutdown)
	config := &schemas.WarpConfig{Enabled: true, Provider: schemas.OpenAI, Model: "gpt-4o"}

	first, cancelFirst := context.WithCancel(context.Background())
	instance, err := client.instanceFor(first, config)
	require.NoError(t, err)
	require.NotNil(t, instance)

	// The request that built the instance goes away. Cancellation propagates
	// through a watcher goroutine, so give it time to land rather than racing it.
	cancelFirst()
	require.Eventually(t, func() bool { return first.Err() != nil }, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	second, err := client.instanceFor(context.Background(), config)
	require.NoError(t, err)
	require.Same(t, instance, second, "the cached instance should be reused")

	// UpdateProvider refuses once the instance context is done, which is the
	// observable form of "this cached client is dead".
	require.NoError(t, second.UpdateProvider(schemas.OpenAI),
		"the cached instance must not be torn down with the request that built it")
}

// Core drops any OpenAI-transport key whose value is empty before a request is
// attempted, so "Any key" (an empty reference) must still produce a value the
// selector accepts. The bearer is a placeholder: the receiving Bifrost only
// reads bearers carrying the virtual-key prefix, so anything else is ignored.
func TestWarpAccountKeyAlwaysCarriesAValue(t *testing.T) {
	for _, keyID := range []string{"", "key-123"} {
		account := &warpAccount{config: &schemas.WarpConfig{Provider: schemas.OpenAI, Model: "gpt-5.5", APIKeyID: keyID}}
		keys, err := account.GetKeysForProvider(nil, schemas.OpenAI)
		require.NoError(t, err)
		require.Len(t, keys, 1)
		require.NotEmpty(t, keys[0].Value.GetValue(), "api_key_id=%q must not yield an empty key value", keyID)
		require.Equal(t, schemas.WhiteList{"*"}, keys[0].Models)
	}
}

// Pinning a provider key is a header concern on the receiving Bifrost
// (x-bf-api-key-id), not a bearer concern - governance ignores a bearer that is
// not a virtual key. The header is emitted only when a key is actually pinned.
func TestWarpRequestHeadersPinSelectedKey(t *testing.T) {
	pinned := requestHeaders(&schemas.WarpConfig{APIKeyID: "key-123"}, "conv-1")
	require.Equal(t, []string{"key-123"}, pinned[PinnedKeyHeader])
	require.Equal(t, []string{"conv-1"}, pinned[ConversationHeader])
	require.Equal(t, []string{"conv-1"}, pinned[SessionHeader])
	require.Equal(t, []string{UserAgent}, pinned["User-Agent"])

	anyKey := requestHeaders(&schemas.WarpConfig{}, "")
	_, hasPin := anyKey[PinnedKeyHeader]
	require.False(t, hasPin, "no key pinned means no pin header")
	_, hasConversation := anyKey[ConversationHeader]
	require.False(t, hasConversation, "no conversation means no grouping header")
	require.Equal(t, []string{UserAgent}, anyKey["User-Agent"], "Warp's traffic is always labelled")
}
