package maps

import "log/slog"

type Client struct {
	apiKey string
	logger *slog.Logger
}

func NewClient(apiKey string, logger *slog.Logger) *Client {
	return &Client{apiKey: apiKey, logger: logger}
}

func (c *Client) LookupAddress(address string) (string, error) {
	c.logger.Debug("lookup address", "address", address)
	return "latitude=0.000000 longitude=0.000000", nil
}
