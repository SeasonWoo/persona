package node

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// What /read asks the assistant to run (#11803, assistant/feishu_read.go, prompts/bot/read_group.md): read a group as
// the user and download its images and files into a directory of their own. lark-cli rates the resource download
// "Risk: write" (it writes a local file), so the gate reads it as a read: none of these may raise a Lark card. Ids are
// placeholders. A subshell (`( cd … && lark-cli … )`) stays opaque to the gate and asks, so the prompt does not use one.
var larkReadGroupCommands = []struct{ name, command string }{
	{"list and download after cd", `mkdir -p ~/.persona/lark-read/om_1 && cd ~/.persona/lark-read/om_1 && lark-cli im +chat-messages-list --chat-id oc_1 --page-size 40 --no-reactions --download-resources`},
	{"a range, to a file", `mkdir -p ~/.persona/lark-read/om_1 && cd ~/.persona/lark-read/om_1 && lark-cli im +chat-messages-list --chat-id oc_1 --start 2026-11-18T00:00:00+08:00 --order asc --page-all --page-limit 10 --page-size 50 --no-reactions --download-resources > messages.json`},
	{"a topic's messages", `cd ~/.persona/lark-read/om_1 && lark-cli im +threads-messages-list --thread omt_1 --page-size 50 --download-resources`},
	{"one image", `cd ~/.persona/lark-read/om_1 && lark-cli im +messages-resources-download --message-id om_2 --file-key img_v3_x --type image --output img_v3_x.jpg`},
	{"one file", `lark-cli im +messages-resources-download --message-id om_3 --file-key file_v3_y --type file`},
	{"files in a loop", `cd ~/.persona/lark-read/om_1 && for k in file_v3_a file_v3_b; do lark-cli im +messages-resources-download --message-id om_3 --file-key $k --type file; echo "$k exit=$?"; done`},
}

// The download's own risk, as lark-cli 1.0.91 prints it: a read for the gate, whatever lark-cli says or does not say;
// a high-risk rating is left as it is.
func TestLarkCLIResourceDownloadIsRead(t *testing.T) {
	for _, risk := range []string{"write", ""} {
		v := classifyLark(`lark-cli im +messages-resources-download --message-id om_1 --file-key img_v3_x --type image`, nil, "", func([]string) string { return risk })
		if len(v.calls) != 1 || v.calls[0].Risk != "read" || v.cliWrite {
			t.Fatalf("risk %q: verdict = %+v", risk, v)
		}
	}
	v := classifyLark(`lark-cli im +messages-resources-download --message-id om_1 --file-key img_v3_x --type image`, nil, "", func([]string) string { return "high-risk-write" })
	if len(v.calls) != 1 || v.calls[0].Risk != "high-risk-write" {
		t.Fatalf("a high-risk rating: verdict = %+v", v)
	}
	// Other downloads and the writes next to it stay writes.
	for _, cmd := range []string{
		`lark-cli drive +download --file-token AAA`,
		`lark-cli im +messages-send --chat-id oc_1 --text hi`,
		`lark-cli im +messages-resources-upload --file a.png`,
	} {
		v := classifyLark(cmd, nil, "", func([]string) string { return "write" })
		if len(v.calls) != 1 || v.calls[0].Risk != "write" || !v.cliWrite {
			t.Fatalf("%s: verdict = %+v", cmd, v)
		}
	}
}

// In the assistant's bypass mode the commands run without a card.
func TestLarkReadGroupCommandsBypass(t *testing.T) {
	for _, tc := range larkReadGroupCommands {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, 0) })
	}
}

// Outside bypass, with lark-cli rating the download a write, no Lark card is raised either: the gate lets a lone call
// through and leaves a command with cd / mkdir to the conversation's own permission mode.
func TestLarkReadGroupCommandsDefaultMode(t *testing.T) {
	for _, tc := range larkReadGroupCommands {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			cwd := t.TempDir()
			p, c, _ := newFakeCodex(t, n, cwd)
			p.id, c.threadID = testThread, testThread
			n.procs[testThread] = p
			n.larkCfg, n.larkCfgAt = olCfg(), time.Now().Add(time.Hour)
			n.larkCfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
			n.larkRisks = map[string]string{"\x00im +messages-resources-download": "write", "\x00im +chat-messages-list": "read",
				"\x00im +threads-messages-list": "read"}
			n.noteModes(testThread, "default", "")
			fh.answer = func(proto.Interaction) proto.Resolution { no := false; return proto.Resolution{Allow: &no} }
			raw, _ := json.Marshal(map[string]string{"command": tc.command})
			out, err := n.larkGate(context.Background(), hookInput{SessionID: testThread, Cwd: cwd, ToolInput: raw}, proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			decision := ""
			if out != nil {
				decision, _ = out.(map[string]any)["hookSpecificOutput"].(map[string]any)["permissionDecision"].(string)
			}
			if cards := len(fh.interactions()); cards != 0 || decision == "deny" {
				t.Fatalf("decision=%q cards=%d; want no card", decision, cards)
			}
		})
	}
}
