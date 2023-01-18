package wsocket

import (
	"bytes"
	"context"
	"errors"
	"io"

	mqtt "github.com/soypat/natiu-mqtt"
	"github.com/soypat/peasocket"
)

var ErrNoMessages = errors.New("No messages")

// PClient uses peasocket implementation.
type PClient struct {
	mq  *mqtt.Client
	trp transporter
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
		trp: transporter{ws: ps},
		mq:  mqtt.NewClient(cfg.MQTTDecoder),
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
			OnSuccessfulTx: func(tx *mqtt.Tx) {
				trp := tx.TxTransport().(*transporter)
				trp.Flush()
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

type transporter struct {
	txbuf       bytes.Buffer
	nextMessage io.Reader
	ws          *peasocket.Client
}

func (trp *transporter) Write(b []byte) (int, error) {
	return trp.txbuf.Write(b)
}

func (trp *transporter) Flush() {
	err := trp.ws.WriteMessage(trp.txbuf.Bytes())
	if err != nil {
		trp.ws.Err()
	}
}

func (trp *transporter) Close() error {
	closeErr := &peasocket.CloseError{
		Status: peasocket.StatusAbnormalClosure,
		Reason: []byte("natiu: closed"),
	}
	trp.ws.CloseWebsocket(closeErr)
	trp.ws.CloseConn(closeErr)
	return nil
}

func (trp *transporter) Read(b []byte) (int, error) {
	if trp.nextMessage == nil {
		N := trp.ws.BufferedMessages()
		if N == 0 {
			return 0, ErrNoMessages
		}
		reader, err := trp.ws.NextMessageReader()
		if err != nil {
			return 0, err
		}
		trp.nextMessage = reader
	}
	n, err := trp.nextMessage.Read(b)
	if err != nil {
		trp.nextMessage = nil
	}
	return n, err
}

func (trp *transporter) BufferedMessages() int {
	return trp.ws.BufferedMessages()
}
