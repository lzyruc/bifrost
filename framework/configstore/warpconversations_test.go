package configstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// newWarpConversationStore returns a store with just the conversation tables.
func newWarpConversationStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableWarpConversation{}, &tables.TableWarpMessage{}))
	return store
}

// seedWarpConversation creates a thread with one exchange.
func seedWarpConversation(t *testing.T, store *RDBConfigStore, ownerID, id, title string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.CreateWarpConversation(ctx, &tables.TableWarpConversation{
		ID: id, OwnerID: ownerID, Title: title, CreatedAt: at, UpdatedAt: at,
	}))
	require.NoError(t, store.AppendWarpMessages(ctx, ownerID, id, []tables.TableWarpMessage{
		{ID: id + "-u", Role: "user", Content: title, CreatedAt: at},
		{ID: id + "-a", Role: "assistant", Content: "answer", CreatedAt: at},
	}))
}

func TestWarpConversationRoundTrip(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "user-1", "c1", "what did we spend?", now)

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "what did we spend?", list[0].Title)

	detail, err := store.GetWarpConversation(ctx, "user-1", "c1")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 2)
	require.Equal(t, "user", detail.Messages[0].Role)
	require.Equal(t, "assistant", detail.Messages[1].Role)
	require.Equal(t, 0, detail.Messages[0].Position)
	require.Equal(t, 1, detail.Messages[1].Position)
}

// The whole access-control story is the owner predicate. A thread must be
// invisible to anyone else, and indistinguishable from one that never existed -
// "that exists but is not yours" confirms another person's thread.
func TestWarpConversationIsScopedToItsOwner(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "user-1", "c1", "mine", now)
	seedWarpConversation(t, store, "user-2", "c2", "theirs", now)

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "c1", list[0].ID)

	_, err = store.GetWarpConversation(ctx, "user-1", "c2")
	require.ErrorIs(t, err, ErrWarpConversationNotFound, "another owner's thread must read as missing")

	require.ErrorIs(t, store.DeleteWarpConversation(ctx, "user-1", "c2"), ErrWarpConversationNotFound)
	require.ErrorIs(t,
		store.AppendWarpMessages(ctx, "user-1", "c2", []tables.TableWarpMessage{{ID: "x", Role: "user", Content: "hi", CreatedAt: now}}),
		ErrWarpConversationNotFound, "appending to another owner's thread must fail")

	// The victim's thread is untouched by all of that.
	detail, err := store.GetWarpConversation(ctx, "user-2", "c2")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 2)
}

// Without user identity every conversation shares one owner, so the history is
// common to the deployment. Same query, different owner - no second code path.
func TestWarpConversationGlobalOwnerSharesHistory(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "__global__", "g1", "shared question", now)

	list, err := store.ListWarpConversations(ctx, "__global__", 50)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// A user-scoped caller must not see the shared history, and vice versa.
	userList, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Empty(t, userList)
}

func TestWarpConversationListIsMostRecentFirst(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	seedWarpConversation(t, store, "user-1", "old", "older", base)
	seedWarpConversation(t, store, "user-1", "new", "newer", base.Add(30*time.Minute))

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "new", list[0].ID)
}

// Appending must bump the thread to the top. Messages that landed while the
// timestamp did not would leave the newest thread sinking down the list, which
// reads as the save having failed.
func TestWarpConversationAppendBumpsOrder(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	seedWarpConversation(t, store, "user-1", "old", "older", base)
	seedWarpConversation(t, store, "user-1", "new", "newer", base.Add(30*time.Minute))

	require.NoError(t, store.AppendWarpMessages(ctx, "user-1", "old", []tables.TableWarpMessage{
		{ID: "old-u2", Role: "user", Content: "follow up", CreatedAt: time.Now().UTC()},
	}))

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Equal(t, "old", list[0].ID, "the thread just appended to must sort first")

	detail, err := store.GetWarpConversation(ctx, "user-1", "old")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 3)
	require.Equal(t, 2, detail.Messages[2].Position, "positions must continue, not restart")
}

func TestWarpConversationDeleteRemovesMessages(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()

	seedWarpConversation(t, store, "user-1", "c1", "delete me", time.Now().UTC())
	require.NoError(t, store.DeleteWarpConversation(ctx, "user-1", "c1"))

	_, err := store.GetWarpConversation(ctx, "user-1", "c1")
	require.ErrorIs(t, err, ErrWarpConversationNotFound)

	// The transcript is the content someone asked to remove; an orphaned row
	// would be a leak of exactly that.
	var orphans int64
	require.NoError(t, store.DB().Model(&tables.TableWarpMessage{}).Where("conversation_id = ?", "c1").Count(&orphans).Error)
	require.Zero(t, orphans)
}

func TestWarpConversationPruneKeepsNewest(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-24 * time.Hour)

	for i := 0; i < 5; i++ {
		seedWarpConversation(t, store, "user-1", string(rune('a'+i)), "thread", base.Add(time.Duration(i)*time.Hour))
	}

	deleted, err := store.PruneWarpConversations(ctx, "user-1", 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), deleted)

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "e", list[0].ID, "the newest must survive")

	var orphans int64
	require.NoError(t, store.DB().Model(&tables.TableWarpMessage{}).Where("conversation_id = ?", "a").Count(&orphans).Error)
	require.Zero(t, orphans, "pruning must take the transcripts with it")
}

func TestWarpConversationPruneIsNoOpBelowLimit(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()

	seedWarpConversation(t, store, "user-1", "c1", "only one", time.Now().UTC())
	deleted, err := store.PruneWarpConversations(ctx, "user-1", 10)
	require.NoError(t, err)
	require.Zero(t, deleted)
}

func TestWarpConversationMessageCounts(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "user-1", "c1", "one", now)
	seedWarpConversation(t, store, "user-1", "c2", "two", now)

	counts, err := store.CountWarpMessages(ctx, []string{"c1", "c2", "missing"})
	require.NoError(t, err)
	require.Equal(t, 2, counts["c1"])
	require.Equal(t, 2, counts["c2"])
	require.NotContains(t, counts, "missing")
}

// Prune selects the stale ids and then deletes them, and between those two
// steps a thread can receive a new message. Deleting it anyway destroys a live
// conversation the owner is actively using, so the delete has to re-assert
// staleness rather than trust the earlier read.
func TestWarpConversationPruneRevalidatesBeforeDeleting(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// Three threads, oldest first; keep=2 makes "old" the prune candidate.
	seedWarpConversation(t, store, "u-1", "old", "oldest", base)
	seedWarpConversation(t, store, "u-1", "mid", "middle", base.Add(time.Hour))
	seedWarpConversation(t, store, "u-1", "new", "newest", base.Add(2*time.Hour))

	// The candidate is used again, which is exactly the race: it is no longer
	// among the oldest, so it must survive.
	require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "old", []tables.TableWarpMessage{
		{ID: "old-u2", Role: "user", Content: "still here", CreatedAt: base.Add(3 * time.Hour)},
	}))

	_, err := store.PruneWarpConversations(ctx, "u-1", 2)
	require.NoError(t, err)

	survived, err := store.GetWarpConversation(ctx, "u-1", "old")
	require.NoError(t, err, "a thread that was used again must not be pruned as stale")
	require.NotNil(t, survived)

	var messages int64
	require.NoError(t, store.DB().Model(&tables.TableWarpMessage{}).Where("conversation_id = ?", "old").Count(&messages).Error)
	require.EqualValues(t, 3, messages, "its messages must survive with it")
}

// Positions order the transcript. Two appends that both read the same COUNT
// assign the same positions, and the reopened thread then renders its messages
// interleaved or out of order with no way to tell which came first.
func TestWarpConversationAppendAssignsUniquePositions(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateWarpConversation(ctx, &tables.TableWarpConversation{
		ID: "c-1", OwnerID: "u-1", Title: "t", CreatedAt: at, UpdatedAt: at,
	}))

	for i := 0; i < 5; i++ {
		require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "c-1", []tables.TableWarpMessage{
			{ID: fmt.Sprintf("m-%d-u", i), Role: "user", Content: "q", CreatedAt: at.Add(time.Duration(i) * time.Minute)},
			{ID: fmt.Sprintf("m-%d-a", i), Role: "assistant", Content: "a", CreatedAt: at.Add(time.Duration(i) * time.Minute)},
		}))
	}

	var rows []tables.TableWarpMessage
	require.NoError(t, store.DB().Where("conversation_id = ?", "c-1").Order("position ASC").Find(&rows).Error)
	require.Len(t, rows, 10)
	seen := map[int]bool{}
	for i, row := range rows {
		require.False(t, seen[row.Position], "position %d assigned twice", row.Position)
		seen[row.Position] = true
		require.Equal(t, i, row.Position, "positions must be a dense sequence in insertion order")
	}
}

