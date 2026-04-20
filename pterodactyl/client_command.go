package pterodactyl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

func (c *Client) clientDoRequest(method, path string, body interface{}) (*http.Response, error) {
	if c.ClientKey == "" {
		return nil, fmt.Errorf("PTERODACTYL_CLIENT_KEY not configured")
	}

	var reqBody *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	} else {
		reqBody = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.ClientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return c.HTTPClient.Do(req)
}

func (c *Client) sendPowerAction(identifier, action string) error {
	body := map[string]string{"signal": action}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal power action body: %w", err)
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/api/client/servers/"+identifier+"/power", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.ClientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("power action: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("power action: status %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) SendCommand(identifier, command string) error {
	if c.ClientKey == "" {
		return fmt.Errorf("PTERODACTYL_CLIENT_KEY not configured: required for sending commands")
	}
	body := map[string]string{"command": command}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal command body: %w", err)
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/api/client/servers/"+identifier+"/command", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.ClientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send command: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("send command: status %d", resp.StatusCode)
	}

	return nil
}
