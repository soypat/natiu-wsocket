package wsocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	mqtt "github.com/soypat/natiu-mqtt"
	"nhooyr.io/websocket"
)

var (
	ErrNotConnected = errors.New("client not connected")
)

type Client struct {
	mqc     mqtt.Client
	cfg     ClientConfig
	msg     *messenger
	lastCtx context.Context
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

func (c *Client) Connect(ctx context.Context) error {
	// A large part of this function is websocket setup and callback setup.
	// The actual packet sending happens after the configuration.
	if c.IsConnected() {
		return errors.New("already connected")
	}
	c.lastCtx = ctx
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
	// Setup Rx.
	rxtx.OnRxError = func(r *mqtt.Rx, err error) {
		c.abnormalDisconnect(err)
	}

	rxtx.SetRxTransport(&clientReader{Client: c})

	// Setup Tx.
	rxtx.OnTxError = func(tx *mqtt.Tx, err error) {
		c.abnormalDisconnect(err)
	}
	rxtx.OnSuccessfulTx = func(tx *mqtt.Tx) {
		// Websocket Writers accumulate writes until Close is called.
		// After Close called the writer flushes contents onto the network.
		// This means we have to set the transport before each message.
		err := tx.CloseTx()
		if err != nil {
			tx.OnTxError(tx, err) // This SHOULD be defined! If it is not: bug.
		}
		c.msg.w = nil
	}
	rxtx.SetTxTransport(c.msg.mustTx())
	// Ready to start sending packet now.

	varconn := c.varconnect()
	// TODO: Add Connack SP logic.
	_, err = c.mqc.Connect(&varconn)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	if !c.IsConnected() {
		return ErrNotConnected
	}
	c.lastCtx = ctx
	rxtx := c.rxtx()
	rxtx.SetTxTransport(c.msg.mustTx())
	return c.mqc.Ping()
}

func (c *Client) Subscribe() error {
	if !c.IsConnected() {
		return ErrNotConnected
	}
	rxtx := c.rxtx()
	rxtx.SetTxTransport(c.msg.mustTx())
	return c.mqc.Ping()
}

func (c *Client) abnormalDisconnect(err error) {
	log.Println("abnormal disconnect call:", err)
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

func (msr *messenger) mustTx() io.WriteCloser {
	w, err := msr.NewTx()
	if err != nil || w == nil {
		panic(err)
	}
	return w
}

func (msr *messenger) NewTx() (io.WriteCloser, error) {
	if msr.HasTx() {
		return nil, errors.New("last message not yet sent")
	}
	w, err := msr.ws.Writer(context.Background(), websocket.MessageBinary)
	msr.w = w
	return w, err
}

func (msr *messenger) HasTx() bool { return msr.w != nil }

type clientReader struct {
	*Client
	buf []byte
}

func (cr *clientReader) Close() error {
	cr.buf = nil
	return nil
}

func (cr *clientReader) Read(p []byte) (int, error) {
	if !cr.IsConnected() {
		return 0, ErrNotConnected
	}
	if len(cr.buf) > 0 {
		// Pre read of contents.
		n := copy(p, cr.buf)
		cr.buf = cr.buf[n:]
		if len(cr.buf) == 0 {
			cr.buf = nil
		}
		if n == len(p) {
			return n, nil
		}
	}
	mt, b, err := cr.msg.ws.Read(cr.lastCtx)
	if err != nil || len(b) == 0 { // TODO(soypat): len(b)==0, this check OK?
		return 0, err
	}
	if mt != websocket.MessageBinary {
		return 0, errors.New("expected binary message")
	}
	if len(cr.buf) == 0 {
		cr.buf = b
	} else {
		cr.buf = append(cr.buf, b...)
	}
	n := copy(p, cr.buf)
	cr.buf = cr.buf[n:]
	if len(cr.buf) == 0 {
		cr.buf = nil
	}
	return n, nil
}
