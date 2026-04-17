package pterodactyl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// SendCommand sends a command to a server using the Client API.
// The Client API uses the server identifier (short ID), not the UUID.
// This requires PTERODACTYL_CLIENT_KEY to be set.
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
