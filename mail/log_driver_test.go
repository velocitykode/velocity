package mail

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestLogDriverSend(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		From("sender@example.com", "Sender").
		To("recipient@example.com").
		Subject("Test Subject").
		Body("Test body")

	err := driver.Send(context.Background(), msg)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	log := driver.GetLog()
	if len(log) != 1 {
		t.Errorf("Expected 1 log entry, got %d", len(log))
	}

	entry := log[0]
	if !strings.Contains(entry, "sender@example.com") {
		t.Errorf("Expected log to contain sender email: %s", entry)
	}
	if !strings.Contains(entry, "recipient@example.com") {
		t.Errorf("Expected log to contain recipient email: %s", entry)
	}
	if !strings.Contains(entry, "Test Subject") {
		t.Errorf("Expected log to contain subject: %s", entry)
	}
}

func TestLogDriverSendWithCC(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		To("to@example.com").
		CC("cc@example.com").
		Subject("Test")

	driver.Send(context.Background(), msg)

	log := driver.GetLog()
	entry := log[0]

	if !strings.Contains(entry, "CC: cc@example.com") {
		t.Errorf("Expected log to contain CC: %s", entry)
	}
}

func TestLogDriverSendWithBCC(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		To("to@example.com").
		BCC("bcc@example.com").
		Subject("Test")

	driver.Send(context.Background(), msg)

	log := driver.GetLog()
	entry := log[0]

	if !strings.Contains(entry, "BCC: bcc@example.com") {
		t.Errorf("Expected log to contain BCC: %s", entry)
	}
}

func TestLogDriverSendWithReplyTo(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		To("to@example.com").
		ReplyTo("reply@example.com").
		Subject("Test")

	driver.Send(context.Background(), msg)

	log := driver.GetLog()
	entry := log[0]

	if !strings.Contains(entry, "Reply-To: reply@example.com") {
		t.Errorf("Expected log to contain Reply-To: %s", entry)
	}
}

func TestLogDriverSendWithHTMLBody(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		To("to@example.com").
		Subject("Test").
		HTMLBody("<h1>HTML Content</h1>")

	driver.Send(context.Background(), msg)

	log := driver.GetLog()
	entry := log[0]

	if !strings.Contains(entry, "HTML Body:") {
		t.Errorf("Expected log to contain HTML Body: %s", entry)
	}
}

func TestLogDriverSendWithAttachments(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		To("to@example.com").
		Subject("Test").
		Body("Body").
		AttachData([]byte("data1"), "file1.txt", "text/plain").
		AttachData([]byte("data2"), "file2.pdf", "application/pdf")

	driver.Send(context.Background(), msg)

	log := driver.GetLog()
	entry := log[0]

	if !strings.Contains(entry, "Attachments: file1.txt, file2.pdf") {
		t.Errorf("Expected log to contain attachments: %s", entry)
	}
}

func TestLogDriverSendMultipleRecipients(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		To("user1@example.com").
		To("user2@example.com").
		Subject("Test")

	driver.Send(context.Background(), msg)

	log := driver.GetLog()
	entry := log[0]

	if !strings.Contains(entry, "user1@example.com") {
		t.Errorf("Expected log to contain user1: %s", entry)
	}
	if !strings.Contains(entry, "user2@example.com") {
		t.Errorf("Expected log to contain user2: %s", entry)
	}
}

func TestLogDriverClearLog(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		To("test@example.com").
		Subject("Test")

	driver.Send(context.Background(), msg)

	if len(driver.GetLog()) != 1 {
		t.Error("Expected 1 log entry before clear")
	}

	driver.ClearLog()

	if len(driver.GetLog()) != 0 {
		t.Error("Expected 0 log entries after clear")
	}
}

func TestLogDriverGetLog(t *testing.T) {
	driver := NewLogDriver()

	for i := 0; i < 3; i++ {
		msg := NewMessage().
			To("test@example.com").
			Subject("Test")
		driver.Send(context.Background(), msg)
	}

	log := driver.GetLog()
	if len(log) != 3 {
		t.Errorf("Expected 3 log entries, got %d", len(log))
	}

	log[0] = "modified"
	log2 := driver.GetLog()
	if log2[0] == "modified" {
		t.Error("GetLog should return a copy, not the original slice")
	}
}

func TestLogDriverRetainsOnlyMostRecentEntries(t *testing.T) {
	driver := NewLogDriver()

	for i := 0; i < logDriverMaxEntries+5; i++ {
		msg := NewMessage().
			To("test@example.com").
			Subject(fmt.Sprintf("Test %03d", i))
		if err := driver.Send(context.Background(), msg); err != nil {
			t.Fatalf("Send failed: %v", err)
		}
	}

	log := driver.GetLog()
	if len(log) != logDriverMaxEntries {
		t.Fatalf("Expected %d log entries, got %d", logDriverMaxEntries, len(log))
	}

	if !strings.Contains(log[0], "Test 005") {
		t.Errorf("Expected first retained entry to be the oldest entry inside the cap, got %q", log[0])
	}
	if !strings.Contains(log[len(log)-1], "Test 104") {
		t.Errorf("Expected last retained entry to be the most recent send, got %q", log[len(log)-1])
	}
}

func TestLogDriverConcurrency(t *testing.T) {
	driver := NewLogDriver()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := NewMessage().
				To("test@example.com").
				Subject("Concurrent Test")
			driver.Send(context.Background(), msg)
		}()
	}

	wg.Wait()

	log := driver.GetLog()
	if len(log) != 100 {
		t.Errorf("Expected 100 log entries, got %d", len(log))
	}
}

func TestLogDriverEmptyMessage(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage()

	err := driver.Send(context.Background(), msg)
	if err != nil {
		t.Errorf("Expected no error for empty message, got %v", err)
	}

	log := driver.GetLog()
	if len(log) != 1 {
		t.Errorf("Expected 1 log entry for empty message, got %d", len(log))
	}
}

func TestLogDriverFromWithName(t *testing.T) {
	driver := NewLogDriver()
	msg := NewMessage().
		From("sender@example.com", "Sender Name").
		To("to@example.com").
		Subject("Test")

	driver.Send(context.Background(), msg)

	log := driver.GetLog()
	entry := log[0]

	if !strings.Contains(entry, `"Sender Name" <sender@example.com>`) {
		t.Errorf("Expected log to contain RFC 5322 quoted from address: %s", entry)
	}
}
