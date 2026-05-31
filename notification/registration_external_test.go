package notification_test

import (
	"testing"

	"github.com/velocitykode/velocity/notification"
	_ "github.com/velocitykode/velocity/notification/standard"
)

func TestStandardRegistersBuiltInChannels(t *testing.T) {
	names := notification.RegisteredChannels()
	registered := make(map[string]bool, len(names))
	for _, name := range names {
		registered[name] = true
	}

	for _, name := range []string{"mail", "database", "slack", "broadcast"} {
		if !registered[name] {
			t.Errorf("expected %s channel to be registered", name)
		}
	}
}
