package warp

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

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
