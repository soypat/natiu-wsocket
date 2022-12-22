package wsocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	mqtt "github.com/soypat/natiu-mqtt"
	"nhooyr.io/websocket"
)

var (
	ErrNotConnected = errors.New("client not connected")
)

// Client shall be safe to use concurrently between two goroutines, one reader (Rx) and
// one that writes (Tx).
type Client struct {
	mqc       *mqtt.Client
	cfg       ClientConfig
	msg       *messenger
	lastRxCtx context.Context
	currentPI uint16
}

type ClientConfig struct {
	// TODO(soypat): Add Will flags/fields?
	URL string
	// MQTT keepalive is amount of seconds between messages before server disconnects client automatically.
	MQTTKeepAlive      uint16
	Username, Password string
	WSOptions          *websocket.DialOptions
	Subs               mqtt.Subscriptions
}

func NewClient(clientID string, config ClientConfig) (*Client, error) {
	if config.Subs == nil {
		return nil, errors.New("nil Sub field in config")
	}
	// Unsubscribe from all.
	config.Subs.Unsubscribe("#", nil)

	const bufsize = 8 * 1024
	mqc := mqtt.NewClient(mqtt.DecoderNoAlloc{UserBuffer: make([]byte, bufsize)})
	mqc.ID = clientID
	c := Client{
		mqc:       mqc,
		cfg:       config,
		currentPI: 1,
	}
	return &c, nil
}

func (c *Client) LastPacketIDSent() uint16 { return c.currentPI - 1 }

func (c *Client) Connect(ctx context.Context) error {
	// A large part of this function is websocket setup and callback setup.
	// The actual packet sending happens after the configuration.
	if c.IsConnected() {
		return errors.New("already connected")
	}
	c.lastRxCtx = ctx
	conn, resp, err := websocket.Dial(ctx, c.cfg.URL, c.cfg.WSOptions)
	if err != nil {
		var answer []byte
		if resp != nil && resp.Body != nil {
			answer, _ = io.ReadAll(resp.Body)
		}
		return fmt.Errorf("ws err:%w. Response if present: %q", err, answer)
	}
	c.msg = &messenger{
		ws: conn,
	}
	rxtx := c.rxtx()
	rxtx.RxCallbacks, rxtx.TxCallbacks = c.callbacks()
	// Setup Rx.
	rxtx.SetRxTransport(&clientReader{Client: c})
	err = c.UnsafePrepareRx(ctx)
	if err != nil {
		return err
	}

	err = c.UnsafePrepareTx(ctx)
	if err != nil {
		return err
	}
	// Ready to start sending packet now.
	varconn := c.varconnect()
	// TODO: Add Connack SP logic.
	_, err = c.mqc.Connect(&varconn)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) callbacks() (mqtt.RxCallbacks, mqtt.TxCallbacks) {
	return mqtt.RxCallbacks{
			OnRxError: func(r *mqtt.Rx, err error) {
				c.abnormalDisconnect(err)
			},
			OnConnack: func(r *mqtt.Rx, vc mqtt.VariablesConnack) error {
				if vc.ReturnCode != 0 {
					return errors.New(vc.ReturnCode.String())
				}
				return nil
			},
		}, mqtt.TxCallbacks{
			OnTxError: func(tx *mqtt.Tx, err error) {
				c.abnormalDisconnect(err)
			},
			OnSuccessfulTx: func(tx *mqtt.Tx) {
				// Websocket Writers accumulate writes until Close is called.
				// After Close called the writer flushes contents onto the network.
				// This means we have to set the transport before each message.
				w := c.msg.w
				if w != nil {
					err := w.Close()
					if err != nil {
						log.Println("error during OnSuccessfulTx closing writer")
					}
				}
				c.msg.w = nil
			},
		}
}

func (c *Client) Ping(ctx context.Context) error {
	if !c.IsConnected() {
		return ErrNotConnected
	}
	err := c.UnsafePrepareTx(ctx)
	if err != nil {
		return err
	}
	err = c.UnsafePrepareRx(ctx)
	if err != nil {
		return err
	}
	return c.mqc.Ping()
}

func (c *Client) Subscribe(ctx context.Context, subReq []mqtt.SubscribeRequest) error {
	if !c.IsConnected() {
		return ErrNotConnected
	}
	err := c.UnsafePrepareTx(ctx)
	if err != nil {
		return err
	}
	err = c.UnsafePrepareRx(ctx)
	if err != nil {
		return err
	}
	suback, err := c.mqc.Subscribe(mqtt.VariablesSubscribe{
		PacketIdentifier: c.newPI(),
		TopicFilters:     subReq,
	})
	if err != nil {
		return err
	}

	if len(subReq) != len(suback.ReturnCodes) {
		return errors.New("length of SUBACK return codes does not match SUBSCRIBE requests")
	}
	if err := suback.Validate(); err != nil {
		return err
	}
	for i, rc := range suback.ReturnCodes {
		if rc.IsValid() {
			err := c.cfg.Subs.Subscribe(subReq[i].TopicFilter)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) PublishPayload(ctx context.Context, topic string, qos mqtt.QoSLevel, payload []byte) error {
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
	err = c.UnsafePrepareTx(ctx)
	if err != nil {
		return err
	}
	// c.lastRxCtx = ctx
	err = c.mqc.PublishPayload(hdr, vp, payload)
	if err != nil {
		return err
	}
	return nil
}

// Disconnect performs a clean disconnect. If the clean disconnect fails it returns an error.
// Even if the clean disconnect fails and Disconnect returns error the result of IsConnected
// will always be false after a call to Disonnect.
func (c *Client) Disconnect(ctx context.Context) error {
	if !c.IsConnected() {
		return nil
	}
	// c.lastRxCtx = ctx
	err := c.UnsafePrepareTx(ctx)
	if err != nil {
		return err
	}
	defer func() { c.msg = nil }()
	err = c.mqc.Disconnect()
	if err != nil {
		c.abnormalDisconnect(fmt.Errorf("during Disconnect: %w", err))
	}
	c.msg = nil
	return err
}

func (c *Client) ReadNextPacket(ctx context.Context) (int, error) {
	if !c.IsConnected() {
		return 0, ErrNotConnected
	}
	rxtx := c.rxtx()
	err := c.UnsafePrepareRx(ctx)
	if err != nil {
		return 0, err
	}
	return rxtx.ReadNextPacket()
}

func (c *Client) SetOnPublishCallback(f func(mqtt.Header, mqtt.VariablesPublish, io.Reader) error) {
	if f == nil {
		c.rxtx().RxCallbacks.OnPub = nil
		return
	}
	c.rxtx().RxCallbacks.OnPub = func(rx *mqtt.Rx, varPub mqtt.VariablesPublish, r io.Reader) error {
		h := rx.LastReceivedHeader
		return f(h, varPub, r)
	}
}

// Prepare Tx must be called before sending a message over the RxTx
// returned by UnsafeRxTx.
func (c *Client) UnsafePrepareTx(ctx context.Context) error {
	msg := c.msg // If msg is edited between here and NewTx no harm is done.
	if !c.IsConnected() || msg == nil {
		return ErrNotConnected
	}
	transport, err := msg.NewTx(ctx)
	if err != nil {
		return err
	}
	rxtx := c.rxtx()
	rxtx.SetTxTransport(transport)
	return nil
}

// Prepare Tx must be called before sending a message over the RxTx
// returned by UnsafeRxTx.
func (c *Client) UnsafePrepareRx(ctx context.Context) error {
	msg := c.msg // If msg is edited between here and NewTx no harm is done.
	if !c.IsConnected() || msg == nil {
		return ErrNotConnected
	}
	c.lastRxCtx = ctx
	return nil
}

// UnsafeRxTx returns the underyling RxTx with callback handlers and all.
// Not safe for concurrent use.
func (c *Client) UnsafeRxTx() *mqtt.RxTx { return c.rxtx() }

func (c *Client) abnormalDisconnect(err error) {
	if c.IsConnected() {
		// log.Println("abnormal disconnect call:", err)
		// TODO, should this error be handled? I think not.
		_ = c.msg.ws.Close(websocket.StatusInternalError, "graceful disconnect")
	} else {
		log.Println("abnormal disconnect call while connected:", err)
	}
	c.msg = nil
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
func (c *Client) newPI() uint16 {
	pi := c.currentPI
	c.currentPI++
	return pi
}

type messenger struct {
	ws *websocket.Conn
	w  io.WriteCloser
}

func (msr *messenger) NewTx(ctx context.Context) (io.WriteCloser, error) {
	if msr.HasTx() {
		return nil, errors.New("last message not yet sent")
	}
	w, err := msr.ws.Writer(ctx, websocket.MessageBinary)
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

func (cr *clientReader) Read(p []byte) (n int, _ error) {
	if !cr.IsConnected() {
		return 0, ErrNotConnected
	}
	if len(cr.buf) > 0 {
		// Pre read of contents.
		n = copy(p, cr.buf)
		cr.buf = cr.buf[n:]
		if len(cr.buf) == 0 {
			cr.buf = nil // Prevent memory leaks.
		}
		if n == len(p) {
			return n, nil
		}
	}
	if err := cr.lastRxCtx.Err(); err != nil {
		return n, err
	}
	mt, b, err := cr.msg.ws.Read(cr.lastRxCtx)
	if err != nil {
		if strings.Contains(err.Error(), "WebSocket closed") {
			cr.abnormalDisconnect(err)
		}
		// This code is not working as expected:
		// if errors.As(err, &websocket.CloseError{}) {
		// 	cr.abnormalDisconnect(err)
		// }
		return n, err
	}
	if mt != websocket.MessageBinary {
		return 0, errors.New("expected binary message")
	}
	if len(cr.buf) == 0 {
		cr.buf = b
	} else {
		cr.buf = append(cr.buf, b...)
	}
	n += copy(p, cr.buf)
	cr.buf = cr.buf[n:]
	if len(cr.buf) == 0 {
		cr.buf = nil
	}
	return n, nil
}
