package pterodactyl

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

func (c *Client) StartServer(identifier string) error {
	return c.sendPowerAction(identifier, "start")
}

func (c *Client) StopServer(identifier string) error {
	return c.sendPowerAction(identifier, "stop")
}

func (c *Client) RestartServer(identifier string) error {
	return c.sendPowerAction(identifier, "restart")
}
