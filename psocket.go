package wsocket

import (
	"context"
	"errors"

	"github.com/soypat/peasocket"
)

// PClient uses peasocket implementation.
type PClient struct {
	client *peasocket.Client
}

type PClientConfig struct {
	ServerURL       string
	WebsocketBuffer []byte
	Entropy         func() uint32
}

func NewPClient(cfg PClientConfig) *PClient {
	ps := peasocket.NewClient(cfg.ServerURL, cfg.WebsocketBuffer, cfg.Entropy)
	pc := &PClient{
		client: ps,
	}
	return pc
}

func (pc *PClient) Connect(ctx context.Context) error {
	if pc.IsConnected() {
		return errors.New("already connected")
	}
	err := pc.client.DialViaHTTPClient(ctx, nil)
	if err != nil {
		return err
	}
	return nil
}

func (pc *PClient) IsConnected() bool { return pc.client.Err() == nil }
