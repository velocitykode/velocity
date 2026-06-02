package notification

import "testing"

func TestMailMessageAction_DropsUnsafeSchemes(t *testing.T) {
	safe := []string{
		"https://example.com/verify",
		"http://example.com/path?q=1",
	}
	for _, u := range safe {
		m := (&MailMessage{}).Action("Confirm", u)
		if got := m.GetAction(); got == nil || got.URL != u {
			t.Errorf("safe URL %q was not preserved: %+v", u, got)
		}
	}

	unsafe := []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"vbscript:msgbox(1)",
		"/relative/path",   // not absolute
		"example.com/path", // no scheme
		"",
	}
	for _, u := range unsafe {
		m := (&MailMessage{}).Action("Click", u)
		act := m.GetAction()
		if act == nil {
			t.Fatalf("action should still exist (text preserved) for %q", u)
		}
		if act.URL != "" {
			t.Errorf("unsafe URL %q must be dropped to empty, got %q", u, act.URL)
		}
		if act.Text != "Click" {
			t.Errorf("action text must be preserved, got %q", act.Text)
		}
	}
}
