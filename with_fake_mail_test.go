package velocity

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/mail/mailtest"
)

func fakeMailMessage(subject string) *contract.Message {
	return contract.NewMessage().
		From("from@example.com").
		To("to@example.com").
		Subject(subject).
		TextBody("body")
}

// (criterion 1) WithFakeMail pre-sets the mailer and Bootstrap keeps it:
// a.Mail is the exact fake after New(). Mirrors WithFakeQueue.
func TestWithFakeMail_PreSetMailerKept(t *testing.T) {
	fake := mailtest.NewFakeMailer()

	a, err := NewTestApp(WithFakeMail(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	if a.Mail != contract.Mailer(fake) {
		t.Fatalf("a.Mail = %p, want pre-set fake %p", a.Mail, fake)
	}
	if a.Services.Mail != contract.Mailer(fake) {
		t.Fatalf("a.Services.Mail = %p, want pre-set fake %p", a.Services.Mail, fake)
	}
}

// (criterion 2) A no-option boot still builds the configured mailer; the guard
// only changes behavior when a test pre-sets one.
func TestWithFakeMail_NoOptionBuildsConfiguredMailer(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	if a.Mail == nil {
		t.Fatal("a.Mail is nil, want configured log mailer")
	}
	if _, ok := a.Mail.(*mailtest.FakeMailer); ok {
		t.Fatal("a.Mail is a FakeMailer without WithFakeMail; mailer construction was skipped unexpectedly")
	}
}

// (criterion 3) A message sent through the booted app lands in the fake and
// AssertSent passes.
func TestWithFakeMail_SentMessageRecorded(t *testing.T) {
	fake := mailtest.NewFakeMailer()

	a, err := NewTestApp(WithFakeMail(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	if err := a.Mail.Send(context.Background(), fakeMailMessage("welcome")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	fake.AssertSent(t, func(m *contract.Message) bool {
		return m.GetSubject() == "welcome"
	})
}
