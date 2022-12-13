package wsmqtt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	mqtt "github.com/soypat/natiu-mqtt"
	"nhooyr.io/websocket"
)

type Client struct {
	mqc mqtt.Client
	cfg ClientConfig
	msg *messenger
}

type ClientConfig struct {
	// TODO(soypat): Add Will flags/fields?

	URL string
	// MQTT keepalive is amount of seconds between messages before server disconnects client automatically.
	MQTTKeepAlive      uint16
	Username, Password string
	WSOptions          *websocket.DialOptions
}

func NewClient(clientID string, cfg ClientConfig) (*Client, error) {
	const bufsize = 8 * 1024
	mqc := mqtt.NewClient(mqtt.DecoderNoAlloc{UserBuffer: make([]byte, bufsize)})
	mqc.ID = clientID
	c := Client{
		mqc: *mqc,
		cfg: cfg,
	}
	return &c, nil
}

func (c *Client) Connect() error {
	// A large part of this function is websocket setup and callback setup.
	// The actual packet sending happens after the configuration.
	if c.IsConnected() {
		return errors.New("already connected")
	}
	ctx := context.Background()
	conn, resp, err := websocket.Dial(ctx, c.cfg.URL, c.cfg.WSOptions)
	if err != nil {
		var answer []byte
		if resp != nil && resp.Body != nil {
			answer, _ = io.ReadAll(resp.Body)
		}
		return fmt.Errorf("ws err:%w. Response if present: %q", err, answer)
	}
	c.msg = &messenger{
		ws: *conn,
	}

	rxtx := c.rxtx()
	rxtx.OnTxError = func(tx *mqtt.Tx, err error) {
		c.erraticDisconnect(err)
	}
	rxtx.OnRxError = func(r *mqtt.Rx, err error) {
		c.erraticDisconnect(err)
	}
	rxtx.OnSuccessfulTx = func(tx *mqtt.Tx) {
		err := c.msg.Send()
		if err != nil {
			log.Println("bug in OnSuccesfulTx:", err)
		}
	}

	mt, rd, err := conn.Reader(ctx)
	if err != nil {
		return err
	}
	if mt != websocket.MessageBinary {
		return errors.New("expected binary message protocol on websocket")
	}
	rxtx.SetRxTransport(io.NopCloser(rd))

	// Ready to start sending packet now.
	rxtx.SetTxTransport(&wcloser{Writer: c.msg.mustTx()})
	varconn := c.varconnect()
	// TODO: Add Connack SP logic.
	_, err = c.mqc.Connect(&varconn)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) erraticDisconnect(err error) {
	if c.IsConnected() {
		err := c.msg.ws.Close(websocket.StatusInternalError, "graceful disconnect")
		if err != nil {
			log.Println("error during graceful disconnect:", err)
		}
		c.msg = nil
	}
}

func (c *Client) IsConnected() bool { return c.msg != nil }

func (c *Client) varconnect() (varConn mqtt.VariablesConnect) {
	varConn.SetDefaultMQTT([]byte(c.mqc.ID))
	varConn.KeepAlive = c.cfg.MQTTKeepAlive
	varConn.Username = []byte(c.cfg.Username)
	varConn.Password = []byte(c.cfg.Password)
	return varConn
}

func (c *Client) rxtx() *mqtt.RxTx { return c.mqc.UnsafeRxTxPointer() }

type messenger struct {
	ws websocket.Conn
	w  io.WriteCloser
}

func (msr *messenger) mustTx() io.Writer {
	w, err := msr.NewTx()
	if err != nil || w == nil {
		panic(err)
	}
	return w
}

func (msr *messenger) NewTx() (io.Writer, error) {
	if msr.HasTx() {
		return nil, errors.New("last message not yet sent")
	}
	w, err := msr.ws.Writer(context.Background(), websocket.MessageBinary)
	msr.w = w
	return w, err
}

func (msr *messenger) Send() error {
	if !msr.HasTx() {
		return errors.New("no message to send or last message errored")
	}
	defer func() { msr.w = nil }()
	return msr.w.Close()
}

func (msr *messenger) HasTx() bool { return msr.w != nil }

// wcloser implements WriterCloser interface from an io.Writer.
// It calls f on Close and returns it's error. f can be nil in which case nil is returned by Close.
type wcloser struct {
	io.Writer
	f func() error
}

func (wc *wcloser) Close() error {
	if wc.f != nil {
		return wc.f()
	}
	return nil
}
