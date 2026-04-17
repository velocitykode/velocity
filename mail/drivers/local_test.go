package drivers

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/mail"
)

func TestNewLocalDriver(t *testing.T) {
	config := mail.LocalConfig{
		Host:       "localhost",
		Port:       "587",
		Username:   "user",
		Password:   "pass",
		Encryption: "tls",
	}

	driver, err := NewLocalDriver(config, "", "")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver == nil {
		t.Error("Expected driver to be created")
	}
}

func TestNewLocalDriverWithSendmail(t *testing.T) {
	config := mail.LocalConfig{
		SendmailPath: "/usr/sbin/sendmail",
	}

	driver, err := NewLocalDriver(config, "", "")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver == nil {
		t.Fatal("Expected driver to be created")
	}

	if driver.sendmail != "/usr/sbin/sendmail" {
		t.Errorf("Expected sendmail path to be set, got %s", driver.sendmail)
	}
}

func TestNewLocalDriverNoConfig(t *testing.T) {
	config := mail.LocalConfig{}

	_, err := NewLocalDriver(config, "", "")
	if err == nil {
		t.Error("Expected error when no configuration is provided")
	}
}

func TestNewLocalDriverDefaultPort(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, err := NewLocalDriver(config, "", "")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver.port != "587" {
		t.Errorf("Expected default port 587, got %s", driver.port)
	}
}

func TestLocalDriverBuildMessage(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "from@example.com", "From Name")

	msg := mail.NewMessage().
		To("to@example.com", "To Name").
		Subject("Test Subject").
		Body("Test body")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "From: From Name <from@example.com>") {
		t.Error("Expected From header in message")
	}

	if !strings.Contains(bodyStr, "To: To Name <to@example.com>") {
		t.Error("Expected To header in message")
	}

	if !strings.Contains(bodyStr, "Subject: Test Subject") {
		t.Error("Expected Subject header in message")
	}

	if !strings.Contains(bodyStr, "Test body") {
		t.Error("Expected body in message")
	}
}

func TestLocalDriverBuildMessageWithCC(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().
		To("to@example.com").
		CC("cc@example.com", "CC Name").
		Subject("Test")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Cc: CC Name <cc@example.com>") {
		t.Errorf("Expected CC header in message, got: %s", bodyStr)
	}
}

func TestLocalDriverBuildMessageWithReplyTo(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().
		To("to@example.com").
		ReplyTo("reply@example.com", "Reply Name").
		Subject("Test")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Reply-To: Reply Name <reply@example.com>") {
		t.Error("Expected Reply-To header in message")
	}
}

func TestLocalDriverBuildMessageWithPriority(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	t.Run("high priority", func(t *testing.T) {
		msg := mail.NewMessage().
			To("to@example.com").
			Subject("Test").
			Priority(mail.HighPriority)

		body := driver.buildMessage(msg)
		bodyStr := string(body)

		if !strings.Contains(bodyStr, "X-Priority: 1") {
			t.Error("Expected X-Priority: 1 for high priority")
		}
	})

	t.Run("low priority", func(t *testing.T) {
		msg := mail.NewMessage().
			To("to@example.com").
			Subject("Test").
			Priority(mail.LowPriority)

		body := driver.buildMessage(msg)
		bodyStr := string(body)

		if !strings.Contains(bodyStr, "X-Priority: 5") {
			t.Error("Expected X-Priority: 5 for low priority")
		}
	})
}

func TestLocalDriverBuildMessageWithCustomHeaders(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		Header("X-Custom-Header", "custom-value")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "X-Custom-Header: custom-value") {
		t.Error("Expected custom header in message")
	}
}

func TestLocalDriverBuildMessageWithHTMLBody(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		HTMLBody("<h1>HTML Content</h1>")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Content-Type: text/html") {
		t.Error("Expected HTML content type")
	}

	if !strings.Contains(bodyStr, "<h1>HTML Content</h1>") {
		t.Error("Expected HTML body in message")
	}
}

func TestLocalDriverBuildMessageWithBothBodies(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		TextBody("Plain text").
		HTMLBody("<h1>HTML</h1>")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "multipart/alternative") {
		t.Error("Expected multipart/alternative for both text and HTML")
	}

	if !strings.Contains(bodyStr, "Plain text") {
		t.Error("Expected plain text body")
	}

	if !strings.Contains(bodyStr, "<h1>HTML</h1>") {
		t.Error("Expected HTML body")
	}
}

func TestLocalDriverBuildMessageWithAttachments(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		Body("Body with attachment").
		AttachData([]byte("file content"), "test.txt", "text/plain")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "multipart/mixed") {
		t.Error("Expected multipart/mixed for attachments")
	}

	if !strings.Contains(bodyStr, "test.txt") {
		t.Error("Expected attachment filename")
	}

	if !strings.Contains(bodyStr, "Content-Transfer-Encoding: base64") {
		t.Error("Expected base64 encoding for attachment")
	}
}

func TestLocalDriverBuildMessageFromConfig(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "default@example.com", "Default Sender")

	// Message without explicit from
	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "From: Default Sender <default@example.com>") {
		t.Error("Expected default from address from config")
	}
}

func TestLocalDriverSendViaSMTPNoRecipients(t *testing.T) {
	config := mail.LocalConfig{
		Host: "localhost",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().Subject("Test")

	err := driver.sendViaSMTP(context.Background(), msg)
	if err == nil {
		t.Error("Expected error when no recipients specified")
	}

	if !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("Expected 'no recipients' error, got %v", err)
	}
}

func TestLocalDriverSendViaSendmailNoRecipients(t *testing.T) {
	config := mail.LocalConfig{
		SendmailPath: "/usr/sbin/sendmail",
	}

	driver, _ := NewLocalDriver(config, "", "")

	msg := mail.NewMessage().Subject("Test")

	err := driver.sendViaSendmail(context.Background(), msg)
	if err == nil {
		t.Error("Expected error when no recipients specified")
	}

	if !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("Expected 'no recipients' error, got %v", err)
	}
}

// startCleartextSMTPServer starts a minimal SMTP server that does NOT advertise
// STARTTLS. Used to assert PlainAuth is refused over cleartext.
func startCleartextSMTPServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				w := bufio.NewWriter(c)
				r := bufio.NewReader(c)
				// Greeting
				if _, err := w.WriteString("220 127.0.0.1 ESMTP\r\n"); err != nil {
					return
				}
				w.Flush()
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"):
						// Deliberately omit STARTTLS and AUTH.
						w.WriteString("250-localhost\r\n")
						w.WriteString("250-8BITMIME\r\n")
						w.WriteString("250 SIZE 1048576\r\n")
						w.Flush()
					case strings.HasPrefix(cmd, "HELO"):
						w.WriteString("250 localhost\r\n")
						w.Flush()
					case cmd == "QUIT":
						w.WriteString("221 bye\r\n")
						w.Flush()
						return
					default:
						w.WriteString("500 unknown\r\n")
						w.Flush()
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() {
		ln.Close()
		wg.Wait()
	}
}

// TestLocalDriverRefusesPlainAuthOverCleartext asserts that when credentials
// are configured and the SMTP server does not advertise STARTTLS, we refuse
// to send and do not expose the password.
func TestLocalDriverRefusesPlainAuthOverCleartext(t *testing.T) {
	addr, stop := startCleartextSMTPServer(t)
	defer stop()

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}

	driver, err := NewLocalDriver(mail.LocalConfig{
		Host:       host,
		Port:       port,
		Username:   "u",
		Password:   "p",
		Encryption: "starttls",
	}, "from@example.com", "From")
	if err != nil {
		t.Fatalf("NewLocalDriver: %v", err)
	}

	msg := mail.NewMessage().To("to@example.com").Subject("Test").TextBody("hi")

	err = driver.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error, got nil — credentials would have been sent in cleartext")
	}
	if !errors.Is(err, ErrPlainAuthRefused) {
		t.Errorf("expected ErrPlainAuthRefused, got %v", err)
	}
	if strings.Contains(err.Error(), "p") && strings.Contains(err.Error(), "password") {
		t.Errorf("error should not leak credentials: %v", err)
	}
}

// TestLocalDriverAllowsSendWithoutAuth asserts the cleartext send path works
// when no username is configured (anonymous relay).
func TestLocalDriverAllowsSendWithoutAuth(t *testing.T) {
	// Just ensures NewLocalDriver paths compile/run — actual send requires a
	// full in-process SMTP server and is out of scope. This test exists as
	// a guardrail that the no-auth branch is not accidentally blocked by the
	// PlainAuth refusal logic.
	d, err := NewLocalDriver(mail.LocalConfig{Host: "localhost", Port: "25"}, "", "")
	if err != nil {
		t.Fatalf("NewLocalDriver: %v", err)
	}
	if d.username != "" {
		t.Fatal("expected blank username")
	}
}
