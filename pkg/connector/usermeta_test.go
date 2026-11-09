package connector

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

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

func TestForumTopicNotifySettings(t *testing.T) {
	mock := tgmock.NewRequire(t)
	tc := &TelegramClient{client: telegram.NewClient(1, "test", telegram.Options{
		Middlewares: []telegram.Middleware{telegram.MiddlewareFunc(func(tg.Invoker) telegram.InvokeFunc {
			return mock.Invoke
		})},
	})}
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
}
