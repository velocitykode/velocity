package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/httpclient"
	"github.com/velocitykode/velocity/internal/neturl"
	"github.com/velocitykode/velocity/notification"
)

func init() {
	notification.Drivers().Register("slack", func(_ context.Context, _ notification.ChannelConfig) (notification.Channel, error) {
		return NewSlackChannel(), nil
	})
}

// SlackChannel delivers notifications via Slack incoming webhooks. The
// channel relies on httpclient.Client for outbound traffic so every dial
// re-runs the SSRF guard against the same private-range allowlist that
// validateWebhookURL applies up-front. The httpclient guard pins the
// first resolved IP into the dial, which closes the DNS-rebinding gap
// where a public-looking hostname could TOCTOU into a metadata or
// RFC1918 address between validation and the real connection.
type SlackChannel struct {
	client *httpclient.Client
}

// NewSlackChannel creates a new Slack notification channel backed by the
// framework's hardened httpclient (private-IP deny on by default, TLS
// minimum enforced, redirects capped, sensitive headers stripped on
// cross-origin hops).
func NewSlackChannel() *SlackChannel {
	return &SlackChannel{
		client: httpclient.New(httpclient.WithTimeout(15 * time.Second)),
	}
}

// Send delivers a notification via Slack.
func (c *SlackChannel) Send(ctx context.Context, notifiable interface{}, n notification.Notification) error {
	sn, ok := n.(notification.SlackNotification)
	if !ok {
		return fmt.Errorf("notification: %T does not implement SlackNotification", n)
	}

	slackMsg := sn.ToSlack(notifiable)
	if slackMsg == nil {
		return nil
	}

	// Get webhook URL from notification route
	webhookURL := ""
	if nr, ok := notifiable.(notification.Notifiable); ok {
		webhookURL = nr.NotificationRoute("slack")
	}
	if webhookURL == "" {
		return fmt.Errorf("notification: no Slack webhook URL for notifiable")
	}

	// Validate webhook URL up-front to fail fast on obviously bad
	// targets. The httpclient dial guard repeats the check on every
	// outbound connection, so this validate-then-dial pair never has a
	// DNS-rebinding gap: if the second resolve returns a private IP,
	// the dial refuses the connection even though validateWebhookURL
	// has already returned ok.
	if err := validateWebhookURL(webhookURL); err != nil {
		return err
	}

	// Build Slack API payload
	payload := c.buildPayload(slackMsg)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notification: failed to marshal Slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("notification: failed to create Slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("notification: Slack request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notification: Slack API returned status %d", resp.StatusCode)
	}

	return nil
}

// buildPayload builds a Slack incoming webhook payload.
func (c *SlackChannel) buildPayload(msg *notification.SlackMessage) map[string]interface{} {
	payload := map[string]interface{}{
		"text": msg.Text,
	}

	if msg.Channel != "" {
		payload["channel"] = msg.Channel
	}

	if msg.Username != "" {
		payload["username"] = msg.Username
	}

	if msg.IconEmoji != "" {
		payload["icon_emoji"] = msg.IconEmoji
	}

	if msg.IconURL != "" {
		payload["icon_url"] = msg.IconURL
	}

	if len(msg.Attachments) > 0 {
		attachments := make([]map[string]interface{}, len(msg.Attachments))
		for i, att := range msg.Attachments {
			a := map[string]interface{}{}
			if att.Color != "" {
				a["color"] = att.Color
			}
			if att.Title != "" {
				a["title"] = att.Title
			}
			if att.TitleLink != "" {
				a["title_link"] = att.TitleLink
			}
			if att.Text != "" {
				a["text"] = att.Text
			}
			if att.Footer != "" {
				a["footer"] = att.Footer
			}
			if att.Timestamp > 0 {
				a["ts"] = att.Timestamp
			}
			if len(att.Fields) > 0 {
				fields := make([]map[string]interface{}, len(att.Fields))
				for j, f := range att.Fields {
					fields[j] = map[string]interface{}{
						"title": f.Title,
						"value": f.Value,
						"short": f.Short,
					}
				}
				a["fields"] = fields
			}
			attachments[i] = a
		}
		payload["attachments"] = attachments
	}

	return payload
}

// validateWebhookURL is a defence-in-depth pre-check that rejects
// obviously bad webhook URLs (private/internal addresses, non-http(s)
// schemes) before any HTTP machinery is engaged. The real SSRF guard is
// inside httpclient.Client: every dial re-resolves and refuses private
// destinations, and the URL-host gate runs on Do() and on every
// redirect hop. Without that second layer, a public hostname could
// resolve to a public IP here and to 169.254.169.254 a millisecond
// later when the real connection is dialled.
func validateWebhookURL(rawURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := neturl.ValidateURLHost(ctx, nil, rawURL); err != nil {
		if errors.Is(err, neturl.ErrPrivateHost) {
			return fmt.Errorf("velocity/notification: webhook url must not target private or internal addresses: %w", err)
		}
		return fmt.Errorf("velocity/notification: invalid webhook url: %w", err)
	}
	return nil
}
