package pterodactyl

import (
	"fmt"
)

type PterodactylServer struct {
	ServerID   int           `json:"server_id"`
	Identifier string        `json:"identifier"`
	UUID       string        `json:"uuid"`
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Container  ContainerInfo `json:"container"`
}

type ContainerInfo struct {
	UUID        string            `json:"uuid"`
	Environment map[string]string `json:"environment"`
}

type listServersResponse struct {
	Data []struct {
		Attributes PterodactylServer `json:"attributes"`
	} `json:"data"`
}

type getServerResponse struct {
	Attributes PterodactylServer `json:"attributes"`
}

func (c *Client) ListServers() ([]PterodactylServer, error) {
	var resp listServersResponse
	if err := c.get("/api/application/servers", &resp); err != nil {
		return nil, err
	}

	servers := make([]PterodactylServer, len(resp.Data))
	for i, s := range resp.Data {
		servers[i] = s.Attributes
	}
	return servers, nil
}

func (c *Client) GetServer(uuid string) (*PterodactylServer, error) {
	var resp getServerResponse
	if err := c.get("/api/application/servers/"+uuid, &resp); err != nil {
		return nil, err
	}
	return &resp.Attributes, nil
}

func (c *Client) StartServer(uuid string) error {
	return c.post(fmt.Sprintf("/api/application/servers/%s/start", uuid), nil, nil)
}

func (c *Client) StopServer(uuid string) error {
	return c.post(fmt.Sprintf("/api/application/servers/%s/stop", uuid), nil, nil)
}

func (c *Client) RestartServer(uuid string) error {
	return c.post(fmt.Sprintf("/api/application/servers/%s/restart", uuid), nil, nil)
}
