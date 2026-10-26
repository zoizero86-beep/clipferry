package core

import (
	"testing"

	"github.com/bkrdmrcioglu/clipferry/internal/store"
)

// concealedClipboard behaves like a clipboard that a password manager has
// written to: the change counter moves and the content is marked concealed.
type concealedClipboard struct {
	fakeClipboard
	count     int64
	concealed bool
}

func (c *concealedClipboard) ChangeCount() int64 { return c.count }
func (c *concealedClipboard) Concealed() bool    { return c.concealed }

func TestConcealedClipboardContentIsTreatedAsSecret(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cb := &concealedClipboard{}
	e, err := New(Options{Store: st, Clipboard: cb, DeviceName: "Mac"})
	if err != nil {
		t.Fatal(err)
	}

	// An ordinary-looking password that the detector alone would not flag.
	cb.SetText("correct horse battery staple")
	cb.count, cb.concealed = 1, true
	e.pollClipboard()
	clips, _ := st.ListClips(store.Query{})
	if len(clips) != 1 || !clips[0].Sensitive || clips[0].SecretKind != "password-manager" || clips[0].ExpiresAt == nil {
		t.Fatalf("concealed copy not handled as a secret: %+v", clips)
	}

	// Unchanged counter: the clipboard is not even read again.
	cb.SetText("something else")
	e.pollClipboard()
	if clips, _ = st.ListClips(store.Query{}); len(clips) != 1 {
		t.Fatalf("poll read the clipboard although its change counter did not move")
	}

	cb.count, cb.concealed = 2, false
	e.pollClipboard()
	if clips, _ = st.ListClips(store.Query{}); len(clips) != 2 || clips[0].Sensitive {
		t.Fatalf("ordinary copy after a concealed one: %+v", clips)
	}
}
