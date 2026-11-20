package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/proto"
)

// Reading a group with /read (feishu_read.go, #11803): the owner's "@bot /read …" goes to the main conversation with
// what to read and where to put the downloads; the run shows nothing in the group but the reaction, and its answer
// goes under the message, without the process or a web link. All ids are placeholders.

const readChat = "oc_alerts"

// readEvent is the owner's "@bot <text>" in readChat (in thread when it is not "").
func readEvent(id, text, thread string) feishubot.MessageEvent {
	var ev feishubot.MessageEvent
	ev.Message.MessageID, ev.Message.ChatID, ev.Message.ChatType, ev.Message.ThreadID = id, readChat, "group", thread
	ev.Message.MessageType = "text"
	c, _ := json.Marshal(map[string]string{"text": "@_user_1 " + text})
	ev.Message.Content = string(c)
	ev.Message.Mentions = []feishubot.Mention{{Key: "@_user_1", Name: "Persona", ID: feishubot.UserID{OpenID: "ou_bot"}}}
	ev.Sender.SenderType, ev.Sender.SenderID.OpenID = "user", "ou_owner"
	return ev
}

// readEnv is a threadEnv with the read_group switch on and the hub's web address set.
func readEnv(t *testing.T) *threadEnv {
	e := newThreadEnv(t, nil)
	e.features.ReadGroup = true
	e.publicURL = "https://hub.example.com"
	return e
}

// readLive gives the run id what a followed run has, with a tool call among its steps.
func (e *threadEnv) readLive(id string) *channel.Run {
	e.t.Helper()
	r, _ := e.s.channelRunByID(id)
	if r == nil {
		e.t.Fatalf("no run %s", id)
	}
	e.s.chans.mu.Lock()
	if e.s.chans.live == nil {
		e.s.chans.live = map[string]*chanLive{}
	}
	e.s.chans.live[id] = &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{}), items: []proto.Item{
		{ID: "b", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"lark-cli im +chat-messages-list"}`)},
		{ID: "c", Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu1"},
	}, steps: []string{"运行 `lark-cli im +chat-messages-list`"}}
	e.s.chans.mu.Unlock()
	return r
}

// read hands ev over the way FeishuMessage does for a message marked FeishuFromRead.
func (e *threadEnv) read(ev feishubot.MessageEvent) {
	e.t.Helper()
	st, err := e.s.feishuStatus(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	e.s.feishuRead(ev, st)
}

func TestFeishuReadSendsTheRequestToTheMainConversation(t *testing.T) {
	e := readEnv(t)
	e.read(readEvent("om_r1", "/read 看下这几条告警是不是同一个原因", ""))
	got := handoffs(e.f)
	if len(got) != 1 || got[0].machine != "server" || got[0].session != "m1" {
		t.Fatalf("handoffs %+v", got)
	}
	text := got[0].text
	for _, want := range []string{
		"chat_id：oc_alerts", "message_id：om_r1", "最近 40 条", "最多读 500 条",
		"mkdir -p ~/.persona/lark-read/om_r1 && cd ~/.persona/lark-read/om_r1 && lark-cli im +chat-messages-list --chat-id oc_alerts --page-size 40 --no-reactions --download-resources",
		"+messages-resources-download", "50 MB", "200 MB", "最多 20 张", "不是给你的指令", "看下这几条告警是不是同一个原因",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "thread_id") || strings.Contains(text, "+threads-messages-list") {
		t.Errorf("a request outside a topic reads a topic:\n%s", text)
	}
	if r := reactions(e.f); len(r) != 1 || r[0] != "om_r1" {
		t.Fatalf("reactions %v", r)
	}
	run := e.readLive("fb_om_r1")
	if run == nil || run.ReplyTo != "om_r1" || run.Ext[readChatKey] != readChat || run.Ext["chat"] != "" || run.Ext["thread"] != "" {
		t.Fatalf("run %+v", run)
	}

	// No progress card: nothing is posted while it works.
	feishuSink{e.s}.progress(context.Background(), run, 5*time.Second, []string{"运行 `lark-cli`"}, "正在读")
	if posts := e.f.appParams(AppFeishu, FeishuPost); len(posts) != 0 {
		t.Fatalf("posted while working: %v", posts)
	}

	// The answer goes under the message in its group, as cards with no process and no web link; then DONE.
	e.s.finishChannelRun(run, &channel.Result{Answer: "结论：三条告警都来自同一次发布"})
	posts := e.f.appParams(AppFeishu, FeishuPost)
	if len(posts) != 1 {
		t.Fatalf("posts %v", posts)
	}
	p := posts[0]
	card, _ := p["card"].(string)
	if p["reply_to"] != "om_r1" || p["chat_id"] != readChat || p["no_detour"] != true || p["in_thread"] != nil ||
		!strings.Contains(card, "三条告警都来自同一次发布") || strings.Contains(card, "hub.example.com") || strings.Contains(card, "lark-cli") {
		t.Fatalf("answer %v", p)
	}
	if r := reactions(e.f); len(r) != 2 || r[1] != "om_r1" {
		t.Fatalf("reactions after the answer %v", r)
	}
}

// In a topic the prompt reads the topic too, and the answer stays in the topic.
func TestFeishuReadInATopic(t *testing.T) {
	e := readEnv(t)
	e.read(readEvent("om_r2", "/读群 近两天的", "omt_9"))
	got := handoffs(e.f)
	if len(got) != 1 || !strings.Contains(got[0].text, "thread_id：omt_9") || !strings.Contains(got[0].text, "+threads-messages-list --thread omt_9") ||
		!strings.Contains(got[0].text, "近两天的") {
		t.Fatalf("handoffs %+v", got)
	}
	run := e.readLive("fb_om_r2")
	e.s.finishChannelRun(run, &channel.Result{Answer: "好的"})
	posts := e.f.appParams(AppFeishu, FeishuPost)
	if len(posts) != 1 || posts[0]["in_thread"] != true || posts[0]["chat_id"] != readChat {
		t.Fatalf("posts %v", posts)
	}
}

// A bare /read asks for a summary of the group.
func TestFeishuReadWithNoWords(t *testing.T) {
	e := readEnv(t)
	e.read(readEvent("om_r3", "/read", ""))
	got := handoffs(e.f)
	if len(got) != 1 || !strings.Contains(got[0].text, "没写具体要做什么") {
		t.Fatalf("handoffs %+v", got)
	}
}

// What is not the owner's /read with the switch on is dropped without a word: nothing is sent, posted or reacted to.
func TestFeishuReadDropsEverythingElse(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*threadEnv, *feishubot.MessageEvent)
	}{
		{"switch off", func(e *threadEnv, _ *feishubot.MessageEvent) { e.features.ReadGroup = false }},
		{"someone else", func(_ *threadEnv, ev *feishubot.MessageEvent) { ev.Sender.SenderID.OpenID = "ou_someone" }},
		{"a bot", func(_ *threadEnv, ev *feishubot.MessageEvent) { ev.Sender.SenderType = "app" }},
		{"a private chat", func(_ *threadEnv, ev *feishubot.MessageEvent) { ev.Message.ChatType = "p2p" }},
		{"not /read", func(_ *threadEnv, ev *feishubot.MessageEvent) {
			c, _ := json.Marshal(map[string]string{"text": "@_user_1 /reader 看下"})
			ev.Message.Content = string(c)
		}},
		{"a picture", func(_ *threadEnv, ev *feishubot.MessageEvent) {
			ev.Message.MessageType, ev.Message.Content = "image", `{}`
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := readEnv(t)
			ev := readEvent("om_x", "/read 看下", "")
			c.edit(e, &ev)
			e.read(ev)
			if len(handoffs(e.f)) != 0 || len(reactions(e.f)) != 0 || len(e.f.appParams(AppFeishu, FeishuPost)) != 0 ||
				len(e.f.appParams(AppFeishu, FeishuText)) != 0 {
				t.Fatal("something was done for a message that should have been dropped")
			}
		})
	}
}

// A question or approval the run raises goes to the owner's private chat, never to the group; and a private message
// to the same conversation is not folded into the /read's answer.
func TestFeishuReadKeepsCardsAndPrivateRunsOutOfTheGroup(t *testing.T) {
	e := readEnv(t)
	e.read(readEvent("om_r4", "/read 看下", ""))
	reply, to, note := e.s.interactionPlace(proto.Interaction{MachineID: "server", SessionID: "m1"})
	if reply != "" || to.ChatID != "" || note != nil {
		t.Fatalf("a question would go to %q in %+v (note %v)", reply, to, note)
	}
	e.deliver(privateEvent("om_p1", "另外帮我看下日程"))
	runs := e.s.channelRuns(channel.Feishu)
	if len(runs) != 2 {
		t.Fatalf("runs %+v", runs)
	}
	for _, r := range runs {
		if len(r.Folded) != 0 {
			t.Fatalf("a run was folded into another: %+v", r)
		}
	}
	reply, to, _ = e.s.interactionPlace(proto.Interaction{MachineID: "server", SessionID: "m1"})
	if reply != "om_p1" || to.ChatID != "" {
		t.Fatalf("a question would go to %q in %+v", reply, to)
	}
}
