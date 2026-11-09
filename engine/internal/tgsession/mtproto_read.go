package tgsession

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/outflareai/outflare/engine/internal/connectors/tgapp"
)

// maxDialogPages bounds one read of the chat list (100 chats a page).
const maxDialogPages = 50

// Read opens the session for reading.
func (m MTProto) Read(ctx context.Context, app tgapp.App, stored []byte, read func(Reader) error) ([]byte, error) {
	cl, mem, err := m.client(app, stored, nil)
	if err != nil {
		return nil, err
	}
	err = cl.Run(ctx, func(ctx context.Context) error {
		if _, err := cl.Self(ctx); err != nil {
			return readErr(err)
		}
		return read(mtReader{api: cl.API()})
	})
	return saved(mem), err
}

type mtReader struct{ api *tg.Client }

func readErr(err error) error {
	if d, ok := tgerr.AsFloodWait(err); ok {
		return &WaitError{For: d}
	}
	return classify(err)
}

// names collects who and what a response mentions.
type names struct {
	users    map[int64]*tg.User
	chats    map[int64]*tg.Chat
	channels map[int64]*tg.Channel
}

func collect(users []tg.UserClass, chats []tg.ChatClass) names {
	n := names{users: map[int64]*tg.User{}, chats: map[int64]*tg.Chat{}, channels: map[int64]*tg.Channel{}}
	for _, u := range users {
		if u, ok := u.(*tg.User); ok {
			n.users[u.ID] = u
		}
	}
	for _, c := range chats {
		switch c := c.(type) {
		case *tg.Chat:
			n.chats[c.ID] = c
		case *tg.Channel:
			n.channels[c.ID] = c
		}
	}
	return n
}

func userName(u *tg.User) string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

func (n names) dialog(p tg.PeerClass, top int) (Dialog, tg.InputPeerClass, bool) {
	switch p := p.(type) {
	case *tg.PeerUser:
		u, ok := n.users[p.UserID]
		if !ok {
			return Dialog{}, nil, false
		}
		d := Dialog{Peer: Peer{Kind: "user", ID: u.ID, AccessHash: u.AccessHash}, Title: userName(u),
			Username: strings.ToLower(u.Username), TopMessage: top, Bot: u.Bot || u.Support, Self: u.Self}
		if u.Phone != "" {
			d.Phone = "+" + strings.TrimPrefix(u.Phone, "+")
		}
		if u.Deleted && d.Title == "" {
			d.Title = "Deleted account"
		}
		return d, &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}, true
	case *tg.PeerChat:
		c, ok := n.chats[p.ChatID]
		if !ok {
			return Dialog{}, nil, false
		}
		return Dialog{Peer: Peer{Kind: "group", ID: c.ID}, Title: c.Title, TopMessage: top, Members: c.ParticipantsCount},
			&tg.InputPeerChat{ChatID: c.ID}, true
	case *tg.PeerChannel:
		c, ok := n.channels[p.ChannelID]
		if !ok {
			return Dialog{}, nil, false
		}
		count, _ := c.GetParticipantsCount()
		return Dialog{Peer: Peer{Kind: "channel", ID: c.ID, AccessHash: c.AccessHash}, Title: c.Title, Username: strings.ToLower(c.Username),
				TopMessage: top, Members: count, Broadcast: c.Broadcast && !c.Megagroup},
			&tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash}, true
	}
	return Dialog{}, nil, false
}

func (r mtReader) Dialogs(ctx context.Context, each func(Dialog) (bool, error)) error {
	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100}
	seen := map[string]bool{}
	for page := 0; page < maxDialogPages; page++ {
		res, err := r.api.MessagesGetDialogs(ctx, req)
		if err != nil {
			return readErr(err)
		}
		mod, ok := res.AsModified()
		if !ok {
			return nil
		}
		n := collect(mod.GetUsers(), mod.GetChats())
		dates := map[string]int{}
		for _, m := range mod.GetMessages() {
			if m, ok := m.(interface {
				GetID() int
				GetDate() int
				GetPeerID() tg.PeerClass
			}); ok {
				dates[peerKey(m.GetPeerID())+fmt.Sprint(m.GetID())] = m.GetDate()
			}
		}
		dialogs := mod.GetDialogs()
		var last tg.InputPeerClass
		lastID, lastDate := 0, 0
		for _, dc := range dialogs {
			dl, ok := dc.(*tg.Dialog)
			if !ok {
				continue // a folder
			}
			d, in, ok := n.dialog(dl.Peer, dl.TopMessage)
			if !ok {
				continue
			}
			last, lastID, lastDate = in, dl.TopMessage, dates[peerKey(dl.Peer)+fmt.Sprint(dl.TopMessage)]
			key := peerKey(dl.Peer)
			if seen[key] {
				continue
			}
			seen[key] = true
			more, err := each(d)
			if err != nil || !more {
				return err
			}
		}
		if _, slice := res.(*tg.MessagesDialogsSlice); !slice || len(dialogs) < req.Limit || last == nil {
			return nil
		}
		req.OffsetPeer, req.OffsetID, req.OffsetDate = last, lastID, lastDate
	}
	return nil
}

func peerKey(p tg.PeerClass) string {
	switch p := p.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("u%d:", p.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("g%d:", p.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("c%d:", p.ChannelID)
	}
	return "?"
}

func inputPeer(p Peer) (tg.InputPeerClass, error) {
	switch p.Kind {
	case "user":
		return &tg.InputPeerUser{UserID: p.ID, AccessHash: p.AccessHash}, nil
	case "group":
		return &tg.InputPeerChat{ChatID: p.ID}, nil
	case "channel":
		return &tg.InputPeerChannel{ChannelID: p.ID, AccessHash: p.AccessHash}, nil
	}
	return nil, errors.New("unknown telegram peer kind " + p.Kind)
}

func (r mtReader) History(ctx context.Context, peer Peer, offsetID, minID, limit int) ([]Message, error) {
	in, err := inputPeer(peer)
	if err != nil {
		return nil, err
	}
	res, err := r.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: in, OffsetID: offsetID, MinID: minID, Limit: limit})
	if err != nil {
		return nil, readErr(err)
	}
	mod, ok := res.AsModified()
	if !ok {
		return nil, nil
	}
	n := collect(mod.GetUsers(), mod.GetChats())
	var out []Message
	for _, mc := range mod.GetMessages() {
		switch m := mc.(type) {
		case *tg.Message:
			msg := Message{ID: m.ID, Out: m.Out, Text: m.Message, At: time.Unix(int64(m.Date), 0).UTC()}
			msg.From, msg.FromName = sender(n, m.FromID, m.PeerID)
			if m.EditDate != 0 {
				t := time.Unix(int64(m.EditDate), 0).UTC()
				msg.EditedAt = &t
			}
			if h, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
				msg.ReplyTo = h.ReplyToMsgID
			}
			msg.Media, msg.FileName = media(m.Media)
			out = append(out, msg)
		case *tg.MessageService:
			msg := Message{ID: m.ID, Out: m.Out, Service: true, At: time.Unix(int64(m.Date), 0).UTC(), Text: serviceText(m.Action)}
			msg.From, msg.FromName = sender(n, m.FromID, m.PeerID)
			out = append(out, msg)
		}
	}
	return out, nil
}

// sender is who wrote a message: the sender in a group, else the other
// person in a private chat.
func sender(n names, from, peer tg.PeerClass) (int64, string) {
	if from == nil {
		from = peer
	}
	if u, ok := from.(*tg.PeerUser); ok {
		if usr, ok := n.users[u.UserID]; ok {
			return usr.ID, userName(usr)
		}
		return u.UserID, ""
	}
	return 0, ""
}

func media(m tg.MessageMediaClass) (string, string) {
	switch m := m.(type) {
	case nil, *tg.MessageMediaEmpty:
		return "", ""
	case *tg.MessageMediaPhoto:
		return "photo", ""
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return "file", ""
		}
		kind, name := "file", ""
		for _, a := range doc.Attributes {
			switch a := a.(type) {
			case *tg.DocumentAttributeFilename:
				name = a.FileName
			case *tg.DocumentAttributeVideo:
				kind = "video"
			case *tg.DocumentAttributeAudio:
				if a.Voice {
					kind = "voice"
				} else {
					kind = "audio"
				}
			case *tg.DocumentAttributeSticker:
				kind = "sticker"
			}
		}
		return kind, name
	case *tg.MessageMediaContact:
		return "contact", ""
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "location", ""
	case *tg.MessageMediaPoll:
		return "poll", ""
	case *tg.MessageMediaWebPage:
		return "", ""
	}
	return "other", ""
}

func serviceText(a tg.MessageActionClass) string {
	switch a.(type) {
	case *tg.MessageActionChatCreate, *tg.MessageActionChannelCreate:
		return "Created the group"
	case *tg.MessageActionChatAddUser, *tg.MessageActionChatJoinedByLink, *tg.MessageActionChatJoinedByRequest:
		return "Joined the group"
	case *tg.MessageActionChatDeleteUser:
		return "Left the group"
	case *tg.MessageActionChatEditTitle:
		return "Renamed the group"
	case *tg.MessageActionPhoneCall:
		return "Call"
	case *tg.MessageActionPinMessage:
		return "Pinned a message"
	}
	return "Chat event"
}
