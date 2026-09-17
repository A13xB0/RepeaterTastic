package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

// newDesk creates a hosted identity called Desk and returns its node id and number.
func newDesk(t *testing.T, env *testEnv, tok string) (string, uint32) {
	t.Helper()
	code, desk, _ := call(t, env.srv, "POST", "/api/v1/identities", tok, map[string]any{"long_name": "Desk", "short_name": "DESK"})
	if code != http.StatusCreated {
		t.Fatalf("create desk: %d %v", code, desk)
	}
	id := desk["node_id"].(string)
	num, _ := wire.ParseNodeID(id)
	return id, num
}

func TestMarkReadAndConversationTitles(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	deskID, deskNum := newDesk(t, env, tok)
	alice, bob := uint32(0x0000a11c), uint32(0x00000b0b)
	env.host.DB.SetUser(alice, &pb.User{Id: wire.NodeID(alice), LongName: "Alice", ShortName: "ALI"})
	past := time.Now().Add(-time.Minute).UnixMilli()
	env.host.Messages.Add(deskNum, &mesh.Message{ID: 1, From: wire.NodeID(alice), To: deskID, Text: "hi", Time: past, Direction: "in"})
	env.host.Messages.Add(deskNum, &mesh.Message{ID: 2, From: wire.NodeID(bob), To: deskID, Text: "yo", Time: past + 1, Direction: "in"})

	titles := conversationTitles(t, env, tok, deskID)
	if titles["dm:"+wire.NodeID(alice)] != "Alice" || titles["dm:"+wire.NodeID(bob)] != wire.NodeID(bob) || titles["ch:0"] != "LongFast" {
		t.Fatalf("titles = %v", titles)
	}
	if unread(t, env, tok, deskID) != 2 {
		t.Fatalf("unread before = %d", unread(t, env, tok, deskID))
	}
	key := "dm:" + wire.NodeID(alice)
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/identities/"+deskID+"/conversations/"+key+"/read", tok, nil); code != http.StatusNoContent {
		t.Fatalf("mark read: %d", code)
	}
	if unread(t, env, tok, deskID) != 1 {
		t.Fatalf("unread after = %d", unread(t, env, tok, deskID))
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/identities/"+deskID+"/conversations/%25zz/read", tok, nil); code != http.StatusBadRequest {
		t.Fatalf("bad key: %d", code)
	}
	for path, want := range map[string]int{"/api/v1/identities/zzz/conversations/ch:0/read": 400, "/api/v1/identities/!0000dead/conversations/ch:0/read": 404} {
		if code, _, _ := call(t, env.srv, "POST", path, tok, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

// conversationTitles is an identity's conversations, key to title.
func conversationTitles(t *testing.T, env *testEnv, tok, id string) map[string]string {
	t.Helper()
	_, _, list := call(t, env.srv, "GET", "/api/v1/identities/"+id+"/conversations", tok, nil)
	out := map[string]string{}
	for _, c := range list {
		m := c.(map[string]any)
		out[m["key"].(string)] = m["title"].(string)
	}
	return out
}

// unread is an identity's unread count.
func unread(t *testing.T, env *testEnv, tok, id string) int {
	t.Helper()
	_, _, list := call(t, env.srv, "GET", "/api/v1/identities", tok, nil)
	for _, x := range list {
		if m := x.(map[string]any); m["node_id"] == id {
			return int(m["unread"].(float64))
		}
	}
	t.Fatalf("no identity %s", id)
	return 0
}

func TestNamedChannelTitle(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	deskID, _ := newDesk(t, env, tok)
	call(t, env.srv, "PUT", "/api/v1/identities/"+deskID+"/channels/2", tok, map[string]any{"name": "", "psk": "AQ==", "role": "SECONDARY"})
	call(t, env.srv, "PUT", "/api/v1/identities/"+deskID+"/channels/3", tok, map[string]any{"name": "Ops", "psk": "AQ==", "role": "SECONDARY"})
	titles := conversationTitles(t, env, tok, deskID)
	// An unnamed channel shows the preset's name.
	if titles["ch:2"] != "LongFast" || titles["ch:3"] != "Ops" {
		t.Fatalf("titles = %v", titles)
	}
	id := env.host.Identity(mustNum(t, deskID))
	if got := env.s.conversationTitle(id, "ch:7"); got != "LongFast" {
		t.Fatalf("empty slot title = %q", got)
	}
}

func mustNum(t *testing.T, id string) uint32 {
	t.Helper()
	n, err := wire.ParseNodeID(id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSendMessageChecks(t *testing.T) {
	env := newTestEnv(t, nil)
	tok := env.signIn(t)
	deskID, _ := newDesk(t, env, tok)
	path := "/api/v1/identities/" + deskID + "/messages"
	if code, obj, _ := call(t, env.srv, "POST", path, tok, map[string]any{"to": "bob", "text": "x"}); code != 400 || !strings.Contains(obj["error"].(string), "node id") {
		t.Fatalf("bad to: %d %v", code, obj)
	}
	if code, obj, _ := call(t, env.srv, "POST", path, tok, map[string]any{"text": ""}); code != 400 || obj["error"] != "empty message" {
		t.Fatalf("empty text: %d %v", code, obj)
	}
	if code, _, _ := call(t, env.srv, "POST", path, tok, map[string]any{"text": strings.Repeat("x", 201)}); code != 400 {
		t.Fatalf("long text: %d", code)
	}
	if code, _ := env.do(t, "POST", path, tok, "application/json", "nope"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
	code, m, _ := call(t, env.srv, "POST", path, tok, map[string]any{"text": "hello all", "want_ack": false})
	if code != http.StatusAccepted || m["to"] != "!ffffffff" || m["text"] != "hello all" {
		t.Fatalf("broadcast: %d %v", code, m)
	}
	_, _, msgs := call(t, env.srv, "GET", path+"?conversation=ch:0&limit=9999", tok, nil)
	if len(msgs) != 1 {
		t.Fatalf("channel messages = %v", msgs)
	}
	if code, _, _ := call(t, env.srv, "GET", "/api/v1/identities/!0000dead/messages", tok, nil); code != http.StatusNotFound {
		t.Fatalf("messages of a stranger: %d", code)
	}
	if code, _, _ := call(t, env.srv, "POST", "/api/v1/identities/!0000dead/messages", tok, map[string]any{"text": "x"}); code != http.StatusNotFound {
		t.Fatalf("send from a stranger: %d", code)
	}
}
