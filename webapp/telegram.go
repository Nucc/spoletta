package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The Telegram connector sends and reads messages as a bot, through
// Telegram's Bot API, with the bot's token an admin sets, which BotFather
// gives. The token goes in each call's path, not a header, so the
// connector never names the URL a call went to, only its method.
//
// A bot reads the messages people send it, and those of the groups it is
// in once its privacy mode is off, by asking for its updates; it can't if
// it has a webhook. Telegram keeps them, for a day, until the bot confirms
// them, after which they are gone, so reading them changes nothing and
// confirming them is an operation that changes something.

var telegramConnector = &connector{
	Name:     "telegram",
	Title:    "Telegram",
	Category: "Communication",
	Description: "Telegram, as a bot: send messages to people, groups and channels, edit them, and read the " +
		"messages people send the bot.",
	URL:       "https://api.telegram.org",
	TokenHelp: "The bot's token, as BotFather gives it, such as 123456789:AA… A chat's id is its user's, group's or channel's; a channel's may be its @username.",
	failed:    notOK,
	authorize: func(_ context.Context, req *http.Request, token string) error {
		// The API's URL, then bot and the token, then the method.
		i := strings.LastIndex(req.URL.Path, "/")
		req.URL.Path = req.URL.Path[:i+1] + "bot" + token + req.URL.Path[i:]
		req.URL.RawPath = ""
		return nil
	},
	checkToken: func(token string) error {
		if !tgToken.MatchString(token) {
			return errors.New("a bot's token is its number, a colon and its secret, as BotFather gives it")
		}
		return nil
	},
	Operations: []operation{
		{Name: "get_updates", Title: "Read the bot's messages", Description: "Lists the messages people sent the bot, or in groups it is in, that it hasn't confirmed, the oldest first.",
			Params: []param{{Name: "limit", Type: paramInteger, Description: fmt.Sprintf("How many to list, at most %d; 20 when left out", maxItems)}},
			run:    tgGetUpdates},
		{Name: "get_chat", Title: "Get a chat", Description: "Gets a chat the bot is in, by its id.",
			Params: []param{tgChat}, run: tgGetChat},

		{Name: "send_message", Title: "Send a message", Description: "Sends a message to a chat.", Write: true,
			Params: []param{tgChat, tgText, tgParseMode,
				{Name: "reply_to", Type: paramInteger, Description: "The message to reply to, by its messageId"}},
			run: tgSendMessage},
		{Name: "confirm_updates", Title: "Confirm the bot's messages", Description: "Confirms the messages the bot has read, up to an update, so Telegram forgets them and lists only newer ones.", Write: true,
			Params: []param{{Name: "through", Type: paramInteger, Required: true, Description: "The last update to confirm, by its updateId"}},
			run:    tgConfirmUpdates},
		{Name: "edit_message", Title: "Edit a message", Description: "Changes what a message the bot sent says.", Write: true,
			Params: []param{tgChat, {Name: "message_id", Type: paramInteger, Required: true, Description: "The message, by its messageId"}, tgText, tgParseMode},
			run:    tgEditMessage},
	},
}

var (
	tgToken = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{20,100}$`)
	tgChat  = param{Name: "chat_id", Type: paramString, Required: true, Description: "The chat's id, such as -1001234567890, or a channel's @username",
		Pattern: regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{4,64})$`)}
	tgText      = param{Name: "text", Type: paramString, Required: true, Description: "What it says, up to 4096 characters"}
	tgParseMode = param{Name: "parse_mode", Type: paramString, Enum: []string{"MarkdownV2", "HTML"}, Description: "MarkdownV2 or HTML, for formatting; plain text when left out"}
)

func tgCall(ctx context.Context, c connectorClient, method string, body map[string]any, out any) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, "/"+method, nil, body, &envelope); err != nil {
		return err
	}
	return json.Unmarshal(envelope.Result, out)
}

type tgMessageJSON struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
	Chat      struct {
		ID       int64  `json:"id"`
		Type     string `json:"type"`
		Title    string `json:"title"`
		Username string `json:"username"`
	} `json:"chat"`
	From *struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	} `json:"from"`
	ReplyTo *struct {
		MessageID int64 `json:"message_id"`
	} `json:"reply_to_message"`
}

// A message, with key, chat/messageId, telling it apart as a shared record.
type tgMessage struct {
	Key       string `json:"key"`
	UpdateID  int64  `json:"updateId,omitempty"`
	MessageID int64  `json:"messageId"`
	ChatID    string `json:"chatId"`
	Chat      string `json:"chat"`
	ChatType  string `json:"chatType"`
	From      string `json:"from"`
	Text      string `json:"text"`
	ReplyTo   int64  `json:"replyTo,omitempty"`
	Date      string `json:"date"`
}

func (m tgMessageJSON) trim() tgMessage {
	chat := strconv.FormatInt(m.Chat.ID, 10)
	out := tgMessage{Key: chat + "/" + strconv.FormatInt(m.MessageID, 10), MessageID: m.MessageID, ChatID: chat, ChatType: m.Chat.Type,
		Chat: m.Chat.Title, Text: m.Text, Date: time.Unix(m.Date, 0).UTC().Format(time.RFC3339)}
	if out.Text == "" {
		out.Text = m.Caption
	}
	if out.Chat == "" && m.Chat.Username != "" {
		out.Chat = "@" + m.Chat.Username
	}
	if m.From != nil {
		out.From = strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
		if m.From.Username != "" {
			out.From = "@" + m.From.Username
		}
	}
	if m.ReplyTo != nil {
		out.ReplyTo = m.ReplyTo.MessageID
	}
	return out
}

func tgGetUpdates(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	limit := int64(20)
	if a.has("limit") {
		limit = a.int("limit")
	}
	if limit < 1 || limit > maxItems {
		return nil, fmt.Errorf("limit must be from 1 to %d", maxItems)
	}
	body := map[string]any{"limit": limit, "timeout": 0, "allowed_updates": []string{"message", "channel_post"}}
	var updates []struct {
		UpdateID    int64          `json:"update_id"`
		Message     *tgMessageJSON `json:"message"`
		ChannelPost *tgMessageJSON `json:"channel_post"`
	}
	if err := tgCall(ctx, c, "getUpdates", body, &updates); err != nil {
		return nil, err
	}
	messages := []tgMessage{}
	for _, u := range updates {
		m := u.Message
		if m == nil {
			m = u.ChannelPost
		}
		if m == nil {
			continue
		}
		out := m.trim()
		out.UpdateID = u.UpdateID
		messages = append(messages, out)
	}
	return messages, nil
}

// Asks for updates from the one after through, which confirms those
// before it, and lists none.
func tgConfirmUpdates(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	var updates []json.RawMessage
	body := map[string]any{"offset": a.int("through") + 1, "limit": 1, "timeout": 0}
	if err := tgCall(ctx, c, "getUpdates", body, &updates); err != nil {
		return nil, err
	}
	return map[string]any{"confirmedThrough": a.int("through")}, nil
}

func tgGetChat(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	var chat struct {
		ID          int64  `json:"id"`
		Type        string `json:"type"`
		Title       string `json:"title"`
		Username    string `json:"username"`
		Description string `json:"description"`
	}
	if err := tgCall(ctx, c, "getChat", map[string]any{"chat_id": a.str("chat_id")}, &chat); err != nil {
		return nil, err
	}
	return map[string]any{"id": strconv.FormatInt(chat.ID, 10), "type": chat.Type, "title": chat.Title, "username": chat.Username, "description": chat.Description}, nil
}

func tgSent(ctx context.Context, c connectorClient, method string, body map[string]any, a opArgs) (any, error) {
	if len([]rune(a.str("text"))) > 4096 {
		return nil, errors.New("a Telegram message may be up to 4096 characters")
	}
	if mode := a.str("parse_mode"); mode != "" {
		body["parse_mode"] = mode
	}
	var m tgMessageJSON
	if err := tgCall(ctx, c, method, body, &m); err != nil {
		return nil, err
	}
	return m.trim(), nil
}

func tgSendMessage(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	body := map[string]any{"chat_id": a.str("chat_id"), "text": a.str("text")}
	if a.has("reply_to") {
		body["reply_parameters"] = map[string]any{"message_id": a.int("reply_to")}
	}
	return tgSent(ctx, c, "sendMessage", body, a)
}

func tgEditMessage(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	return tgSent(ctx, c, "editMessageText", map[string]any{"chat_id": a.str("chat_id"), "message_id": a.int("message_id"), "text": a.str("text")}, a)
}
