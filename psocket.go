package wsocket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	mqtt "github.com/soypat/natiu-mqtt"
	"github.com/soypat/peasocket"
)

var (
	errClosure = &peasocket.CloseError{
		Status: peasocket.StatusAbnormalClosure,
		Reason: []byte("natiu: disconnected"),
	}
	ErrNoMessages = errors.New("no messages")
)

// PClient uses peasocket implementation.
type PClient struct {
	mqc     *mqtt.Client
	trp     transporter
	pi      uint16
	subs    mqtt.Subscriptions
	varconn mqtt.VariablesConnect
}

type PClientConfig struct {
	// MQTT Identification string. Must be unique in a server.
	ID              string
	ServerURL       string
	WebsocketBuffer []byte
	Entropy         func() uint32
	MQTTDecoder     mqtt.Decoder
	Subscriptions   mqtt.Subscriptions
	// MQTT server access username.
	Username string
	// MQTT server access password.
	Password      string
	MQTTKeepalive time.Duration
}

func NewPClient(cfg PClientConfig) *PClient {
	if cfg.MQTTDecoder == nil {
		cfg.MQTTDecoder = mqtt.DecoderNoAlloc{UserBuffer: make([]byte, 32*1024)}
	}
	if cfg.Subscriptions == nil {
		cfg.Subscriptions = make(mqtt.SubscriptionsMap)
	}
	if cfg.MQTTKeepalive < 0 {
		panic("negative keepalive")
	}
	ps := peasocket.NewClient(cfg.ServerURL, cfg.WebsocketBuffer, cfg.Entropy)

	var varconn mqtt.VariablesConnect
	varconn.SetDefaultMQTT([]byte(cfg.ID))
	varconn.ClientID = []byte(cfg.ID)
	varconn.KeepAlive = uint16(cfg.MQTTKeepalive / time.Second)
	varconn.Username = []byte(cfg.Username)
	varconn.Password = []byte(cfg.Password)

	pc := &PClient{
		trp:     transporter{ws: ps},
		mqc:     mqtt.NewClient(cfg.MQTTDecoder),
		varconn: varconn,
		subs:    cfg.Subscriptions,
	}
	rxtx := pc.mqc.UnsafeRxTxPointer()
	rxtx.RxCallbacks, rxtx.TxCallbacks = pc.callbacks()
	return pc
}

func (c *PClient) HandleNextPacket() (int, error) {
	if !c.IsConnected() {
		return 0, ErrNotConnected
	}
	return c.mqc.UnsafeRxTxPointer().ReadNextPacket()
}

func (c *PClient) SetOnPublishCallback(f func(mqtt.Header, mqtt.VariablesPublish, io.Reader) error) {
	if f == nil {
		c.mqc.UnsafeRxTxPointer().RxCallbacks.OnPub = nil
		return
	}
	c.mqc.UnsafeRxTxPointer().RxCallbacks.OnPub = func(rx *mqtt.Rx, varPub mqtt.VariablesPublish, r io.Reader) error {
		return f(rx.LastReceivedHeader, varPub, r)
	}
}

func (pc *PClient) Connect(ctx context.Context) error {
	if pc.IsConnected() {
		return errors.New("already connected")
	}
	err := pc.trp.ws.DialViaHTTPClient(ctx, nil)
	if err != nil {
		return err
	}
	connack, err := pc.mqc.Connect(&pc.varconn)
	if err != nil {
		return fmt.Errorf("during connect (%s): %s", connack, err)
	}
	return nil
}

func (c *PClient) Ping() error {
	if !c.IsConnected() {
		return ErrNotConnected
	}
	return c.mqc.Ping()
}

func (c *PClient) Subscribe(subReq []mqtt.SubscribeRequest) error {
	if !c.IsConnected() {
		return ErrNotConnected
	}
	suback, err := c.mqc.Subscribe(mqtt.VariablesSubscribe{
		PacketIdentifier: c.newPI(),
		TopicFilters:     subReq,
	})
	if err != nil {
		return err
	}
	if err := suback.Validate(); err != nil {
		return err
	}
	for i, rc := range suback.ReturnCodes {
		if rc.IsValid() {
			err := c.subs.Subscribe(subReq[i].TopicFilter)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *PClient) PublishPayload(topic string, qos mqtt.QoSLevel, payload []byte) error {
	if !c.IsConnected() {
		return ErrNotConnected
	}
	if qos != mqtt.QoS0 {
		return errors.New("only QoS0 supported")
	}
	pflags, err := mqtt.NewPublishFlags(qos, false, false)
	if err != nil {
		return err
	}
	vp := mqtt.VariablesPublish{
		TopicName:        []byte(topic),
		PacketIdentifier: c.newPI(),
	}
	hdr, err := mqtt.NewHeader(mqtt.PacketPublish, pflags, uint32(vp.Size(qos)+len(payload)))
	if err != nil {
		return err
	}
	err = c.mqc.PublishPayload(hdr, vp, payload)
	if err != nil {
		return err
	}
	return nil
}

// Disconnect performs a clean disconnect. If the clean disconnect fails it returns an error.
// Even if the clean disconnect fails and Disconnect returns error the result of IsConnected
// will always be false after a call to Disonnect.
func (c *PClient) Disconnect(ctx context.Context) error {
	if !c.IsConnected() {
		return nil
	}
	err := c.mqc.Disconnect()
	if err != nil {
		c.closeConn(fmt.Errorf("during Disconnect: %w", err))
	} else {
		c.closeConn(errClosure)
	}
	return err
}

func (pc *PClient) IsConnected() bool { return pc.trp.ws.IsConnected() }

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
	if pc.trp.ws.IsConnected() {
		pc.trp.Close()
	}
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
		log.Println("flushing: ", err)
	}
}

func (trp *transporter) Close() error {
	trp.ws.CloseWebsocket(errClosure)
	trp.ws.CloseConn(errClosure)
	return nil
}

func (trp *transporter) Read(b []byte) (int, error) {
	if trp.nextMessage == nil {
		N := trp.ws.BufferedMessages()
		if N == 0 {
			return 0, ErrNoMessages
		}
		reader, _, err := trp.ws.BufferedMessageReader()
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

// newPI returns current pi and adds one to it.
func (c *PClient) newPI() uint16 {
	pi := c.pi
	c.pi++
	return pi
}
