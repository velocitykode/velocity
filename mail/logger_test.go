package mail

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/log"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
	"github.com/velocitykode/velocity/trace"
)

var _ contract.LoggerAware = (*LogDriver)(nil)

// logDriverMessage is a message with every part the summary names.
func logDriverMessage() *Message {
	return NewMessage().
		From("sender@example.com", "Sender").
		To("to@example.com").
		CC("cc@example.com").
		BCC("bcc@example.com").
		ReplyTo("reply@example.com").
		Subject("Welcome").
		Body("plain body text").
		HTMLBody("<p>html body text</p>")
}

// The log driver writes one info line per message through its logger,
// naming sender, recipients, subject and sizes but never the body, and
// nothing through the standard library log.
func TestLogDriver_SummaryIsOneInfoLineThroughItsLogger(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	out := &fallbacklogtest.Output{}
	d := NewLogDriver()
	d.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))

	if err := d.Send(context.Background(), logDriverMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	lines := out.Lines()
	if len(lines) != 1 || !strings.Contains(lines[0], "INFO: velocity/mail: message logged, not sent (log driver)") {
		t.Fatalf("logger lines = %q, want one info line", lines)
	}
	for _, want := range []string{"to=to@example.com", "cc=cc@example.com", "bcc=bcc@example.com", "reply_to=reply@example.com", "subject=Welcome", "text_bytes=15", "html_bytes=21"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("line %q does not contain %q", lines[0], want)
		}
	}
	if strings.Contains(lines[0], "body text") {
		t.Errorf("line %q contains the body", lines[0])
	}
	if s := stdlib.String(); s != "" {
		t.Errorf("stdlib got %q, want nothing", s)
	}
	if got := len(d.GetLog()); got != 1 {
		t.Errorf("retained entries = %d, want 1", got)
	}
}

// The recipient addresses are key-value values, so a redacting logger
// (LOG_REDACT_EMAILS) masks them.
func TestLogDriver_RecipientsPassThroughTheRedactor(t *testing.T) {
	out := &fallbacklogtest.Output{}
	d := NewLogDriver()
	d.SetLogger(log.WithRedactors(logdrivers.NewConsoleLoggerTo(out, 0), log.EmailRedactor()))

	if err := d.Send(context.Background(), logDriverMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	s := out.String()
	for _, addr := range []string{"sender@example.com", "to@example.com", "cc@example.com", "bcc@example.com", "reply@example.com"} {
		if strings.Contains(s, addr) {
			t.Errorf("redacted line contains %q: %q", addr, s)
		}
	}
	if !strings.Contains(s, "to=[EMAIL]") {
		t.Errorf("line %q does not carry the redacted recipient", s)
	}
}

// NewMailer's mailer hands a logger to the log driver it wraps, the way
// velocity.New's logger sweep reaches it.
func TestNewMailer_LogDriverTakesTheLogger(t *testing.T) {
	m, err := NewMailer(MailConfig{Driver: "log"})
	if err != nil {
		t.Fatalf("NewMailer: %v", err)
	}
	la, ok := m.(contract.LoggerAware)
	if !ok {
		t.Fatalf("NewMailer returned %T, which does not implement contract.LoggerAware", m)
	}
	out := &fallbacklogtest.Output{}
	la.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))

	if err := m.Send(context.Background(), logDriverMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := strings.Count(out.String(), "INFO: velocity/mail: message logged"); got != 1 {
		t.Errorf("logger info lines = %d, want 1 (%q)", got, out.String())
	}
}

// Without a logger the summary goes to the fallback logger, which drops
// info lines: nothing reaches the standard library log or standard error.
func TestLogDriver_WithoutLoggerWritesNothingToStderr(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	d := NewLogDriver()
	if err := d.Send(context.Background(), logDriverMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if s := stdlib.String() + fallback.String(); s != "" {
		t.Errorf("stdlib / fallback got %q, want nothing", s)
	}
}

// SetLogger may run while messages are sent: the logger is held under the
// driver's mutex.
func TestLogDriver_SetLoggerWhileSendingIsSafe(t *testing.T) {
	out := &fallbacklogtest.Output{}
	d := NewLogDriver()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
				d.SetLogger(nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = d.Send(context.Background(), logDriverMessage())
			}
		}()
	}
	wg.Wait()
}

// The summary line carries the request, trace and span ids of the ctx the
// message was sent under.
func TestLogDriver_SummaryCarriesTheRequestIDs(t *testing.T) {
	out := &fallbacklogtest.Output{}
	d := NewLogDriver()
	d.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
	ctx := trace.WithTrace(trace.WithRequestID(context.Background(), "req-mail-1"), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
	if err := d.Send(ctx, logDriverMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if want := "request_id=req-mail-1 trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7"; !strings.Contains(out.String(), want) {
		t.Errorf("line %q does not carry %q", out.String(), want)
	}
}

// SetLogger's edge inputs: nil puts the driver back on the fallback (which
// drops the info line); a zero-value LogDriver takes a logger and sends;
// the mailer NewMailer returns keeps forwarding after Shutdown.
func TestLogDriver_SetLoggerEdgeInputs(t *testing.T) {
	t.Run("nil restores the fallback", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		d := NewLogDriver()
		d.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		d.SetLogger(nil)
		if err := d.Send(context.Background(), logDriverMessage()); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if s := out.String() + fallback.String(); s != "" {
			t.Errorf("replaced logger / fallback got %q, want nothing", s)
		}
		if got := len(d.GetLog()); got != 1 {
			t.Errorf("retained entries = %d, want 1", got)
		}
	})

	t.Run("zero value", func(t *testing.T) {
		out := &fallbacklogtest.Output{}
		var d LogDriver
		d.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		if err := d.Send(context.Background(), logDriverMessage()); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := strings.Count(out.String(), "INFO: velocity/mail: message logged"); got != 1 {
			t.Errorf("logger info lines = %d, want 1 (%q)", got, out.String())
		}
	})

	t.Run("mailer nil and after shutdown", func(t *testing.T) {
		m, err := NewMailer(MailConfig{Driver: "log"})
		if err != nil {
			t.Fatalf("NewMailer: %v", err)
		}
		la := m.(contract.LoggerAware)
		la.SetLogger(nil)
		if err := m.(ShutdownableMailer).Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		out := &fallbacklogtest.Output{}
		la.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		if err := m.Send(context.Background(), logDriverMessage()); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := strings.Count(out.String(), "INFO: velocity/mail: message logged"); got != 1 {
			t.Errorf("logger info lines = %d, want 1 (%q)", got, out.String())
		}
	})
}
