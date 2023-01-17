package wsocket

import (
	"context"
	"errors"

	mqtt "github.com/soypat/natiu-mqtt"
	"github.com/soypat/peasocket"
)

// PClient uses peasocket implementation.
type PClient struct {
	ws *peasocket.Client
	mq *mqtt.Client
}

type PClientConfig struct {
	ServerURL       string
	WebsocketBuffer []byte
	Entropy         func() uint32
	MQTTDecoder     mqtt.Decoder
}

func NewPClient(cfg PClientConfig) *PClient {
	ps := peasocket.NewClient(cfg.ServerURL, cfg.WebsocketBuffer, cfg.Entropy)
	pc := &PClient{
		ws: ps,
		mq: mqtt.NewClient(cfg.MQTTDecoder),
	}
	return pc
}

func (pc *PClient) Connect(ctx context.Context) error {
	if pc.IsConnected() {
		return errors.New("already connected")
	}
	err := pc.ws.DialViaHTTPClient(ctx, nil)
	if err != nil {
		return err
	}
	// pc.mq.SetTransport(pc.ws.)
	return nil
}

func (pc *PClient) IsConnected() bool { return pc.ws.Err() == nil }

func (pc *PClient) callbacks() (mqtt.RxCallbacks, mqtt.TxCallbacks) {
	return mqtt.RxCallbacks{
			OnRxError: func(r *mqtt.Rx, err error) {
				pc.closeConn(err)
			},
			OnConnack: func(r *mqtt.Rx, vc mqtt.VariablesConnack) error {
				if vc.ReturnCode != 0 {
					return errors.New(vc.ReturnCode.String())
				}
				return nil
			},
		}, mqtt.TxCallbacks{
			OnTxError: func(tx *mqtt.Tx, err error) {
				pc.closeConn(err)
			},
		}
}

func (pc *PClient) closeConn(err error) {

}
func (c *PClient) Ping(ctx context.Context) error {
	if !c.IsConnected() {
		return ErrNotConnected
	}

	return c.mq.Ping()
}
