package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/internal/neturl"
	"github.com/velocitykode/velocity/notification"
)

func init() {
	notification.Drivers().Register("slack", func(_ context.Context, _ notification.ChannelConfig) (notification.Channel, error) {
		return NewSlackChannel(), nil
	})
}

// SlackChannel delivers notifications via Slack incoming webhooks.
type SlackChannel struct {
	client *http.Client
}

// NewSlackChannel creates a new Slack notification channel.
func NewSlackChannel() *SlackChannel {
	return &SlackChannel{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
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

	// Validate webhook URL to prevent SSRF attacks
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

	resp, err := c.client.Do(req)
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

// validateWebhookURL validates that a webhook URL is safe to call,
// preventing SSRF attacks against internal/private networks. DNS
// hostnames are resolved and every resolved address is checked against
// the shared private-range guard — so a public-looking name that
// resolves to 127.0.0.1 or an RFC1918 address is still rejected.
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
