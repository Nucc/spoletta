package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// The Slack connector reads and posts messages in a workspace's channels
// through Slack's Web API, with a bot token an admin sets: the app's, which
// sees only the channels it is invited to. Slack answers a call that failed
// with a success saying ok: false, which the connector takes as the failure
// it is.

var slackConnector = &connector{
	Name:     "slack",
	Title:    "Slack",
	Category: "Communication",
	Description: "Slack's channels and messages: list channels, read a channel's messages and a thread's replies, " +
		"find people, post and update messages and react to them.",
	URL: "https://slack.com/api",
	TokenHelp: "A bot token, starting xoxb-, of a Slack app installed in the workspace, with the scopes " +
		"channels:read, channels:history, groups:read, groups:history, chat:write, reactions:write, users:read " +
		"and users:read.email. The app sees only the channels it is invited to.",
	failed: notOK,
	checkToken: func(token string) error {
		if !regexp.MustCompile(`^xox[bpa]-`).MatchString(token) {
			return fmt.Errorf("a Slack token starts xoxb-, the bot's")
		}
		return nil
	},
	Operations: []operation{
		{Name: "list_channels", Title: "List channels", Description: "Lists the workspace's channels the app can see, with their ids.",
			Params: []param{{Name: "private", Type: paramBoolean, Description: "Whether to list the private channels it is in too"}, slLimit},
			run:    slListChannels},
		{Name: "list_messages", Title: "List a channel's messages", Description: "Lists a channel's messages, the latest first.",
			Params: []param{slChannel, {Name: "since", Type: paramString, Description: "Only messages after this one, by its ts, such as 1730000000.000100"}, slLimit},
			run:    slListMessages},
		{Name: "list_replies", Title: "List a thread's replies", Description: "Lists the messages of a thread, the one it started from first.",
			Params: []param{slChannel, slTS("The thread's first message, by its ts"), slLimit},
			run:    slListReplies},
		{Name: "find_user", Title: "Find a person", Description: "Finds someone in the workspace by their email address.",
			Params: []param{{Name: "email", Type: paramString, Required: true, Description: "Their email address"}},
			run:    slFindUser},

		{Name: "post_message", Title: "Post a message", Description: "Posts a message to a channel, or replies in a thread.", Write: true,
			Params: []param{slChannel, slText, {Name: "thread_ts", Type: paramString, Pattern: slTSPattern, Description: "The thread to reply in, by its first message's ts"}},
			run:    slPostMessage},
		{Name: "update_message", Title: "Update a message", Description: "Changes what a message the app posted says.", Write: true,
			Params: []param{slChannel, slTS("The message, by its ts"), slText},
			run:    slUpdateMessage},
		{Name: "add_reaction", Title: "React to a message", Description: "Adds an emoji reaction to a message.", Write: true,
			Params: []param{slChannel, slTS("The message, by its ts"),
				{Name: "emoji", Type: paramString, Required: true, Pattern: regexp.MustCompile(`^[a-z0-9_+'-]{1,100}$`), Description: "The emoji's name, without colons, such as eyes"}},
			run: slAddReaction},
	},
}

var (
	slChannel = param{Name: "channel", Type: paramString, Required: true, Description: "The channel's id, such as C0123456789",
		Pattern: regexp.MustCompile(`^[A-Z0-9]{8,20}$`)}
	slText      = param{Name: "text", Type: paramString, Required: true, Description: "What it says, in Slack's mrkdwn"}
	slTSPattern = regexp.MustCompile(`^[0-9]{6,12}\.[0-9]{6}$`)
	slLimit     = param{Name: "limit", Type: paramInteger, Description: fmt.Sprintf("How many to list, at most %d; 20 when left out", maxItems)}
)

func slTS(description string) param {
	return param{Name: "ts", Type: paramString, Required: true, Pattern: slTSPattern, Description: description}
}

func slLimitOf(a opArgs) (string, error) {
	n := int64(20)
	if a.has("limit") {
		n = a.int("limit")
	}
	if n < 1 || n > maxItems {
		return "", fmt.Errorf("limit must be from 1 to %d", maxItems)
	}
	return strconv.FormatInt(n, 10), nil
}

type slChannelJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"is_private"`
	Members   int    `json:"num_members"`
	Topic     struct {
		Value string `json:"value"`
	} `json:"topic"`
	Purpose struct {
		Value string `json:"value"`
	} `json:"purpose"`
}

type slChannelOut struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Private bool   `json:"private"`
	Members int    `json:"members"`
	Topic   string `json:"topic"`
	Purpose string `json:"purpose"`
}

func slListChannels(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	limit, err := slLimitOf(a)
	if err != nil {
		return nil, err
	}
	types := "public_channel"
	if a.bool("private") {
		types += ",private_channel"
	}
	var out struct {
		Channels []slChannelJSON `json:"channels"`
	}
	q := url.Values{"types": {types}, "limit": {limit}, "exclude_archived": {"true"}}
	if err := c.do(ctx, http.MethodGet, "/conversations.list", q, nil, &out); err != nil {
		return nil, err
	}
	channels := []slChannelOut{}
	for _, ch := range out.Channels {
		channels = append(channels, slChannelOut{ID: ch.ID, Name: ch.Name, Private: ch.IsPrivate, Members: ch.Members, Topic: ch.Topic.Value, Purpose: ch.Purpose.Value})
	}
	return channels, nil
}

type slMessageJSON struct {
	TS         string `json:"ts"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	Text       string `json:"text"`
	ThreadTS   string `json:"thread_ts"`
	ReplyCount int    `json:"reply_count"`
}

// A message, with key, channel/ts, telling it apart as a shared record.
type slMessage struct {
	Key      string `json:"key"`
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
	User     string `json:"user"`
	Text     string `json:"text"`
	ThreadTS string `json:"threadTs"`
	Replies  int    `json:"replies"`
}

func (m slMessageJSON) trim(channel string) slMessage {
	user := m.User
	if user == "" {
		user = m.BotID
	}
	return slMessage{Key: channel + "/" + m.TS, Channel: channel, TS: m.TS, User: user, Text: m.Text, ThreadTS: m.ThreadTS, Replies: m.ReplyCount}
}

func slMessages(ctx context.Context, c connectorClient, method string, q url.Values, channel string) ([]slMessage, error) {
	var out struct {
		Messages []slMessageJSON `json:"messages"`
	}
	if err := c.do(ctx, http.MethodGet, method, q, nil, &out); err != nil {
		return nil, err
	}
	messages := []slMessage{}
	for _, m := range out.Messages {
		messages = append(messages, m.trim(channel))
	}
	return messages, nil
}

func slListMessages(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	limit, err := slLimitOf(a)
	if err != nil {
		return nil, err
	}
	q := url.Values{"channel": {a.str("channel")}, "limit": {limit}}
	if since := a.str("since"); since != "" {
		if !slTSPattern.MatchString(since) {
			return nil, fmt.Errorf("%s isn't a message's ts", quote(since))
		}
		q.Set("oldest", since)
	}
	return slMessages(ctx, c, "/conversations.history", q, a.str("channel"))
}

func slListReplies(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	limit, err := slLimitOf(a)
	if err != nil {
		return nil, err
	}
	return slMessages(ctx, c, "/conversations.replies", url.Values{"channel": {a.str("channel")}, "ts": {a.str("ts")}, "limit": {limit}}, a.str("channel"))
}

func slFindUser(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	var out struct {
		User struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			TZ       string `json:"tz"`
			Profile  struct {
				Email       string `json:"email"`
				DisplayName string `json:"display_name"`
				Title       string `json:"title"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := c.do(ctx, http.MethodGet, "/users.lookupByEmail", url.Values{"email": {a.str("email")}}, nil, &out); err != nil {
		return nil, err
	}
	u := out.User
	return map[string]any{"id": u.ID, "name": u.Name, "realName": u.RealName, "displayName": u.Profile.DisplayName,
		"email": u.Profile.Email, "title": u.Profile.Title, "timeZone": u.TZ}, nil
}

func slPosted(ctx context.Context, c connectorClient, method string, body map[string]any) (any, error) {
	var out struct {
		Channel string        `json:"channel"`
		TS      string        `json:"ts"`
		Message slMessageJSON `json:"message"`
	}
	if err := c.do(ctx, http.MethodPost, method, nil, body, &out); err != nil {
		return nil, err
	}
	m := out.Message
	m.TS = out.TS
	return m.trim(out.Channel), nil
}

func slPostMessage(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	body := map[string]any{"channel": a.str("channel"), "text": a.str("text")}
	if ts := a.str("thread_ts"); ts != "" {
		body["thread_ts"] = ts
	}
	return slPosted(ctx, c, "/chat.postMessage", body)
}

func slUpdateMessage(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	return slPosted(ctx, c, "/chat.update", map[string]any{"channel": a.str("channel"), "ts": a.str("ts"), "text": a.str("text")})
}

func slAddReaction(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	body := map[string]any{"channel": a.str("channel"), "timestamp": a.str("ts"), "name": a.str("emoji")}
	if err := c.do(ctx, http.MethodPost, "/reactions.add", nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"channel": a.str("channel"), "ts": a.str("ts"), "emoji": a.str("emoji")}, nil
}
