package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/retry"
)

// DefaultTelegramAPI is the public Bot API.
const DefaultTelegramAPI = "https://api.telegram.org"

// TelegramConfig configures the Telegram bot.
type TelegramConfig struct {
	BotToken string `json:"botToken"`
	// APIURL allows a self hosted Bot API server.
	APIURL  string `json:"apiUrl"`
	BotName string `json:"botName"`
}

// Configured reports whether messages can be sent.
func (c TelegramConfig) Configured() bool { return c.BotToken != "" }

func (c TelegramConfig) base() string {
	u := strings.TrimRight(c.APIURL, "/")
	if u == "" {
		u = DefaultTelegramAPI
	}
	return u + "/bot" + c.BotToken
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Result      json.RawMessage `json:"result"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// errRetryAfter carries Telegram's flood control delay.
type errRetryAfter struct{ wait time.Duration }

func (e errRetryAfter) Error() string {
	return fmt.Sprintf("telegram rate limit, retry after %s", e.wait)
}

func (c TelegramConfig) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		// Never leak the token embedded in the URL.
		return nil, errors.New(strings.ReplaceAll(err.Error(), c.BotToken, "<token>"))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r tgResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("telegram: unexpected response (HTTP %d)", resp.StatusCode)
	}
	if !r.OK {
		if r.ErrorCode == http.StatusTooManyRequests && r.Parameters.RetryAfter > 0 {
			return nil, errRetryAfter{time.Duration(r.Parameters.RetryAfter) * time.Second}
		}
		err := fmt.Errorf("telegram: %s", r.Description)
		if r.ErrorCode >= 400 && r.ErrorCode < 500 {
			return nil, retry.Permanent(err)
		}
		return nil, err
	}
	return r.Result, nil
}

// GetMe validates the token and returns the bot username.
func (c TelegramConfig) GetMe(ctx context.Context) (string, error) {
	res, err := c.call(ctx, "getMe", struct{}{})
	if err != nil {
		return "", err
	}
	var me struct {
		Username string `json:"username"`
	}
	_ = json.Unmarshal(res, &me)
	return me.Username, nil
}

// SendMessage sends an HTML formatted message, retrying on flood control
// and transient errors.
func (c TelegramConfig) SendMessage(ctx context.Context, chatID, html string) error {
	if !c.Configured() {
		return errors.New("telegram is not configured")
	}
	body := map[string]any{"chat_id": chatID, "text": html, "parse_mode": "HTML", "disable_web_page_preview": true}
	p := retry.Policy{MaxAttempts: 4, Initial: 500 * time.Millisecond, Max: 30 * time.Second}
	return retry.Run(ctx, p, func(ctx context.Context, _ int) error {
		_, err := c.call(ctx, "sendMessage", body)
		var ra errRetryAfter
		if errors.As(err, &ra) {
			if serr := retry.Sleep(ctx, ra.wait); serr != nil {
				return retry.Permanent(serr)
			}
		}
		return err
	})
}
