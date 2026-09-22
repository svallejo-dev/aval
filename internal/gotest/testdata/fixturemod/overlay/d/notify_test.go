package d

import (
	"testing"

	// Does not exist on purpose: the change adds it.
	"example.com/fixturemod/overlay/mail"
)

func TestMessage(t *testing.T) {
	t.Run("ORD-F43 mails the shipping notice", func(t *testing.T) {
		if got := mail.Subject(Message("A1")); got == "" {
			t.Error("empty subject")
		}
	})
}
