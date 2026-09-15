package mesh

import "testing"

func TestMessageStoreTakePut(t *testing.T) {
	a, b := NewMessageStore(10), NewMessageStore(10)
	a.Add(7, &Message{Text: "hello"})
	a.MarkRead(7, "channel:0")
	msgs, read := a.Take(7)
	if len(a.Window(7, 0)) != 0 || len(msgs) != 1 {
		t.Fatalf("take left %d, took %d", len(a.Window(7, 0)), len(msgs))
	}
	b.Put(7, msgs, read)
	if got := b.Window(7, 0); len(got) != 1 || got[0].Text != "hello" || len(b.read[7]) != 1 {
		t.Fatalf("put = %v read %v", got, b.read[7])
	}
}
