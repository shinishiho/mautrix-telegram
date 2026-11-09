package connector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
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
