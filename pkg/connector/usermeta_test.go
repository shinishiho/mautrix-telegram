package connector

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/bin"
	"go.mau.fi/mautrix-telegram/pkg/gotd/telegram"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tgmock"
)

func testDialog(pinned bool, folderID int) *tg.Dialog {
	dialog := &tg.Dialog{}
	dialog.SetPinned(pinned)
	if folderID != 0 {
		dialog.SetFolderID(folderID)
	}
	return dialog
}

func TestFillUserLocalMetaTag(t *testing.T) {
	pinnedCommunity := &tg.DialogCommunity{}
	pinnedCommunity.SetPinned(true)
	for _, test := range []struct {
		name   string
		dialog userLocalDialog
		tag    *event.RoomTag
	}{
		// Untagged chats leave Matrix tags alone during sync.
		{"untagged", testDialog(false, 0), nil},
		{"pinned", testDialog(true, 0), ptr.Ptr(event.RoomTagFavourite)},
		{"archived", testDialog(false, archiveFolderID), ptr.Ptr(event.RoomTagLowPriority)},
		{"pinned inside archive", testDialog(true, archiveFolderID), ptr.Ptr(event.RoomTagLowPriority)},
		{"pinned community", pinnedCommunity, ptr.Ptr(event.RoomTagFavourite)},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := &bridgev2.ChatInfo{}
			new(TelegramClient).fillUserLocalMeta(info, test.dialog)
			assert.Equal(t, test.tag, info.UserLocal.Tag)
		})
	}
}

func newMockClient(mock *tgmock.Mock) *telegram.Client {
	return telegram.NewClient(1, "test", telegram.Options{
		Middlewares: []telegram.Middleware{telegram.MiddlewareFunc(func(tg.Invoker) telegram.InvokeFunc {
			return mock.Invoke
		})},
	})
}

func TestForumTopicNotifySettings(t *testing.T) {
	mock := tgmock.NewRequire(t)
	tc := &TelegramClient{client: newMockClient(mock)}
	channel := &tg.InputPeerChannel{ChannelID: 42, AccessHash: 123}
	var unmuted, mutedGroup tg.PeerNotifySettings
	unmuted.SetMuteUntil(0)
	mutedGroup.SetMuteUntil(math.MaxInt32)

	// An explicit topic setting wins without looking up the group.
	settings, err := tc.forumTopicNotifySettings(t.Context(), channel, unmuted)
	require.NoError(t, err)
	until, ok := settings.GetMuteUntil()
	assert.True(t, ok)
	assert.Equal(t, 0, until)

	// A topic without its own mute follows the group.
	mock.ExpectCall(&tg.AccountGetNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: channel}}).ThenResult(&mutedGroup)
	settings, err = tc.forumTopicNotifySettings(t.Context(), channel, tg.PeerNotifySettings{})
	require.NoError(t, err)
	until, ok = settings.GetMuteUntil()
	assert.True(t, ok)
	assert.Equal(t, math.MaxInt32, until)

	// Other topics in the same group reuse the group's settings.
	settings, err = tc.forumTopicNotifySettings(t.Context(), channel, tg.PeerNotifySettings{})
	require.NoError(t, err)
	until, _ = settings.GetMuteUntil()
	assert.Equal(t, math.MaxInt32, until)

	// Settings updates for the group replace the cached settings.
	tc.cacheGroupNotifySettings(channel.ChannelID, unmuted)
	settings, err = tc.forumTopicNotifySettings(t.Context(), channel, tg.PeerNotifySettings{})
	require.NoError(t, err)
	until, _ = settings.GetMuteUntil()
	assert.Equal(t, 0, until)
}

func TestForumTopicPortalKey(t *testing.T) {
	tc, _ := newTagTestClient(t)
	// The General topic's room is the group's own portal.
	assert.Equal(t, tc.makePortalKeyFromID(ids.PeerTypeChannel, 42, 0), tc.forumTopicPortalKey(42, generalTopicID))
	assert.Equal(t, tc.makePortalKeyFromID(ids.PeerTypeChannel, 42, 7), tc.forumTopicPortalKey(42, 7))
}

func testForumTopic(id int, settings tg.PeerNotifySettings) *tg.ForumTopic {
	return &tg.ForumTopic{
		ID:             id,
		Peer:           &tg.PeerChannel{ChannelID: 42},
		FromID:         &tg.PeerUser{UserID: 1},
		NotifySettings: settings,
	}
}

func TestApplyGeneralTopicMute(t *testing.T) {
	channel := &tg.InputPeerChannel{ChannelID: 42, AccessHash: 123}
	request := &tg.MessagesGetForumTopicsByIDRequest{Peer: channel, Topics: []int{generalTopicID}}
	groupMute := ptr.Ptr(time.Unix(math.MaxInt32, 0))
	var unmuted tg.PeerNotifySettings
	unmuted.SetMuteUntil(0)

	t.Run("own mute", func(t *testing.T) {
		tc, mock := newTagTestClient(t)
		mock.ExpectCall(request).ThenResult(&tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
			testForumTopic(generalTopicID, unmuted),
		}})
		info := &bridgev2.ChatInfo{UserLocal: &bridgev2.UserLocalPortalInfo{MutedUntil: groupMute}}
		tc.applyGeneralTopicMute(t.Context(), channel, info)
		assert.Equal(t, time.Unix(0, 0), *info.UserLocal.MutedUntil)
	})
	t.Run("follows group", func(t *testing.T) {
		tc, mock := newTagTestClient(t)
		mock.ExpectCall(request).ThenResult(&tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
			testForumTopic(generalTopicID, tg.PeerNotifySettings{}),
		}})
		info := &bridgev2.ChatInfo{UserLocal: &bridgev2.UserLocalPortalInfo{MutedUntil: groupMute}}
		tc.applyGeneralTopicMute(t.Context(), channel, info)
		assert.Equal(t, groupMute, info.UserLocal.MutedUntil)
	})
}

func newTagTestClient(t *testing.T, onlyBridgeTags ...event.RoomTag) (*TelegramClient, *tgmock.Mock) {
	mock := tgmock.NewRequire(t)
	return &TelegramClient{
		client: newMockClient(mock),
		main: &TelegramConnector{Bridge: &bridgev2.Bridge{Config: &bridgeconfig.BridgeConfig{
			OnlyBridgeTags: onlyBridgeTags,
		}}},
	}, mock
}

func testChatPortal(tc *TelegramClient, chatID int64) *bridgev2.Portal {
	return &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: tc.makePortalKeyFromID(ids.PeerTypeChat, chatID, 0),
		Metadata:  &PortalMetadata{},
	}}
}

func testChatDialog(chatID int64, pinned bool, folderID int) *tg.Dialog {
	dialog := testDialog(pinned, folderID)
	dialog.Peer = &tg.PeerChat{ChatID: chatID}
	return dialog
}

func TestDialogPeerPortalKey(t *testing.T) {
	tc, _ := newTagTestClient(t)
	key, ok := tc.dialogPeerPortalKey(&tg.DialogPeer{Peer: &tg.PeerChat{ChatID: 1}})
	assert.True(t, ok)
	assert.Equal(t, tc.makePortalKeyFromID(ids.PeerTypeChat, 1, 0), key)
	key, ok = tc.dialogPeerPortalKey(&tg.DialogPeerCommunity{CommunityID: 2})
	assert.True(t, ok)
	assert.Equal(t, tc.makePortalKeyFromID(ids.PeerTypeChannel, 2, 0), key)
	_, ok = tc.dialogPeerPortalKey(&tg.DialogPeerFolder{FolderID: archiveFolderID})
	assert.False(t, ok)
}

func TestReadDialogTagsBatches(t *testing.T) {
	tc, mock := newTagTestClient(t)
	portals := map[networkid.PortalKey]*bridgev2.Portal{}
	for chatID := int64(1); chatID <= getPeerDialogsBatchSize+1; chatID++ {
		portal := testChatPortal(tc, chatID)
		portals[portal.PortalKey] = portal
	}
	var requested []tg.InputDialogPeerClass
	expectBatch := func(dialogs ...tg.DialogClass) {
		mock.ExpectFunc(func(b bin.Encoder) {
			req, ok := b.(*tg.MessagesGetPeerDialogsRequest)
			require.True(t, ok, "unexpected request %T", b)
			requested = append(requested, req.Peers...)
		}).ThenResult(&tg.MessagesPeerDialogs{Dialogs: dialogs})
	}
	// Map order decides which chats share a request, so dialogs are returned
	// independently of what was asked for and matched by peer.
	expectBatch(testChatDialog(1, true, 0), testChatDialog(2, false, archiveFolderID), testChatDialog(3, false, 0))
	expectBatch()

	tags := tc.readDialogTags(t.Context(), portals)
	assert.Len(t, requested, getPeerDialogsBatchSize+1)
	for chatID := int64(1); chatID <= getPeerDialogsBatchSize+1; chatID++ {
		assert.Contains(t, requested, &tg.InputDialogPeer{Peer: &tg.InputPeerChat{ChatID: chatID}})
	}
	assert.Equal(t, map[networkid.PortalKey]event.RoomTag{
		tc.makePortalKeyFromID(ids.PeerTypeChat, 1, 0): event.RoomTagFavourite,
		tc.makePortalKeyFromID(ids.PeerTypeChat, 2, 0): event.RoomTagLowPriority,
		tc.makePortalKeyFromID(ids.PeerTypeChat, 3, 0): "",
	}, tags)
}

func TestReadDialogTagsFailure(t *testing.T) {
	tc, mock := newTagTestClient(t)
	portal := testChatPortal(tc, 1)
	mock.Expect().ThenErr(assert.AnError)
	// Failed lookups are left out so the caller falls back to the update's tag.
	tags := tc.readDialogTags(t.Context(), map[networkid.PortalKey]*bridgev2.Portal{portal.PortalKey: portal})
	assert.Empty(t, tags)
}

func TestHandleRoomTag(t *testing.T) {
	tags := func(tags ...event.RoomTag) *event.TagEventContent {
		content := &event.TagEventContent{Tags: event.Tags{}}
		for _, tag := range tags {
			content.Tags[tag] = event.TagMetadata{}
		}
		return content
	}
	const chatID = 42
	peer := &tg.InputPeerChat{ChatID: chatID}
	dialogPeer := &tg.InputDialogPeer{Peer: peer}
	bothTags := []event.RoomTag{event.RoomTagFavourite, event.RoomTagLowPriority}
	for _, test := range []struct {
		name           string
		onlyBridgeTags []event.RoomTag
		prev, content  *event.TagEventContent
		dialog         *tg.Dialog
		folderID       *int
		pin            *bool
	}{{
		name:    "unrelated tag change",
		prev:    tags("u.work"),
		content: tags("u.work", "u.other"),
	}, {
		name:    "favourite pins",
		prev:    tags(),
		content: tags(event.RoomTagFavourite),
		dialog:  testChatDialog(chatID, false, 0),
		pin:     ptr.Ptr(true),
	}, {
		name:    "removing favourite unpins",
		prev:    tags(event.RoomTagFavourite),
		content: tags(),
		dialog:  testChatDialog(chatID, true, 0),
		pin:     ptr.Ptr(false),
	}, {
		name:     "low priority archives without touching the pin",
		prev:     tags(event.RoomTagFavourite),
		content:  tags(event.RoomTagLowPriority),
		dialog:   testChatDialog(chatID, true, 0),
		folderID: ptr.Ptr(archiveFolderID),
	}, {
		name:     "favouriting an archived chat unarchives and pins it",
		prev:     tags(event.RoomTagLowPriority),
		content:  tags(event.RoomTagFavourite),
		dialog:   testChatDialog(chatID, false, archiveFolderID),
		folderID: ptr.Ptr(0),
		pin:      ptr.Ptr(true),
	}, {
		name:     "removing low priority unarchives",
		prev:     tags(event.RoomTagLowPriority),
		content:  tags(),
		dialog:   testChatDialog(chatID, false, archiveFolderID),
		folderID: ptr.Ptr(0),
	}, {
		name:    "echoed low priority keeps a pin inside the archive",
		prev:    tags(event.RoomTagFavourite),
		content: tags(event.RoomTagLowPriority),
		dialog:  testChatDialog(chatID, true, archiveFolderID),
	}, {
		name:           "archive isn't changed when low priority isn't bridged",
		onlyBridgeTags: []event.RoomTag{event.RoomTagFavourite},
		content:        tags(),
		dialog:         testChatDialog(chatID, false, archiveFolderID),
	}} {
		t.Run(test.name, func(t *testing.T) {
			onlyBridgeTags := test.onlyBridgeTags
			if onlyBridgeTags == nil {
				onlyBridgeTags = bothTags
			}
			tc, mock := newTagTestClient(t, onlyBridgeTags...)
			if test.dialog != nil {
				mock.ExpectCall(&tg.MessagesGetPeerDialogsRequest{Peers: []tg.InputDialogPeerClass{dialogPeer}}).
					ThenResult(&tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{test.dialog}})
			}
			if test.folderID != nil {
				mock.ExpectCall(&tg.FoldersEditPeerFoldersRequest{FolderPeers: []tg.InputFolderPeer{{Peer: peer, FolderID: *test.folderID}}}).
					ThenResult(&tg.Updates{})
			}
			if test.pin != nil {
				mock.ExpectCall(&tg.MessagesToggleDialogPinRequest{Pinned: *test.pin, Peer: dialogPeer}).ThenTrue()
			}
			err := tc.HandleRoomTag(t.Context(), &bridgev2.MatrixRoomTag{
				MatrixEventBase: bridgev2.MatrixEventBase[*event.TagEventContent]{
					Content: test.content,
					Portal:  testChatPortal(tc, chatID),
				},
				PrevContent: test.prev,
			})
			require.NoError(t, err)
		})
	}
}

func TestBridgedTag(t *testing.T) {
	onlyFavourites, _ := newTagTestClient(t, event.RoomTagFavourite)
	// Archiving or unpinning into an archive clears a favourite instead of
	// sending a low priority tag that would be dropped.
	assert.Equal(t, event.RoomTag(""), onlyFavourites.bridgedTag(event.RoomTagLowPriority, event.RoomTagLowPriority))
	assert.Equal(t, event.RoomTag(""), onlyFavourites.bridgedTag(event.RoomTagLowPriority, ""))
	assert.Equal(t, event.RoomTagFavourite, onlyFavourites.bridgedTag(event.RoomTagFavourite, ""))

	onlyLowPriority, _ := newTagTestClient(t, event.RoomTagLowPriority)
	// Unarchiving a pinned chat clears low priority.
	assert.Equal(t, event.RoomTag(""), onlyLowPriority.bridgedTag(event.RoomTagFavourite, ""))
	// Pins have no earlier tag to clear, so they don't touch the room's tags.
	assert.Equal(t, event.RoomTagFavourite, onlyLowPriority.bridgedTag(event.RoomTagFavourite, event.RoomTagFavourite))

	both, _ := newTagTestClient(t, event.RoomTagFavourite, event.RoomTagLowPriority)
	assert.Equal(t, event.RoomTagLowPriority, both.bridgedTag(event.RoomTagLowPriority, ""))
}

func TestSetPinned(t *testing.T) {
	pinned := []networkid.PortalID{"a", "b"}
	assert.Equal(t, []networkid.PortalID{"b"}, setPinned(slices.Clone(pinned), "a", false))
	assert.Equal(t, []networkid.PortalID{"b", "a"}, setPinned(slices.Clone(pinned), "a", true))
	assert.Equal(t, []networkid.PortalID{"a", "b", "c"}, setPinned(slices.Clone(pinned), "c", true))
}

func TestPinnedDialogIDs(t *testing.T) {
	tc, _ := newTagTestClient(t)
	pinnedCommunity := &tg.DialogCommunity{CommunityID: 3}
	pinnedCommunity.SetPinned(true)
	pinnedFolder := &tg.DialogFolder{Peer: &tg.PeerChat{ChatID: 4}}
	pinnedFolder.SetPinned(true)
	assert.Equal(t, []networkid.PortalID{
		tc.makePortalKeyFromID(ids.PeerTypeChat, 1, 0).ID,
		tc.makePortalKeyFromID(ids.PeerTypeChannel, 3, 0).ID,
	}, tc.pinnedDialogIDs([]tg.DialogClass{
		testChatDialog(1, true, 0),
		testChatDialog(2, true, archiveFolderID),
		pinnedCommunity,
		// The folder's peer is its top message's chat, which isn't pinned.
		pinnedFolder,
		testChatDialog(5, false, 0),
	}))
}

func TestFillUserLocalFromDialog(t *testing.T) {
	tc, mock := newTagTestClient(t)
	dialog := testChatDialog(42, true, archiveFolderID)
	dialog.NotifySettings.SetMuteUntil(math.MaxInt32)
	mock.ExpectCall(&tg.MessagesGetPeerDialogsRequest{Peers: []tg.InputDialogPeerClass{
		&tg.InputDialogPeer{Peer: &tg.InputPeerChat{ChatID: 42}},
	}}).ThenResult(&tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{dialog}})
	info := &bridgev2.ChatInfo{}
	tc.fillUserLocalFromDialog(t.Context(), testChatPortal(tc, 42), info)
	// A new room for an archived chat, e.g. one Telegram archived on its
	// first message, starts as low priority.
	assert.Equal(t, ptr.Ptr(event.RoomTagLowPriority), info.UserLocal.Tag)
	assert.Equal(t, ptr.Ptr(time.Unix(math.MaxInt32, 0)), info.UserLocal.MutedUntil)

	// Topic rooms get their mute from the topic instead.
	topic := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: tc.makePortalKeyFromID(ids.PeerTypeChannel, 42, 7),
		Metadata:  &PortalMetadata{},
	}}
	info = &bridgev2.ChatInfo{}
	tc.fillUserLocalFromDialog(t.Context(), topic, info)
	assert.Nil(t, info.UserLocal)
}
