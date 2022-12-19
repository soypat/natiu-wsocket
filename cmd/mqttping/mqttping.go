package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	mqtt "github.com/soypat/natiu-mqtt"
	wsocket "github.com/soypat/natiu-wsocket"
)

const (
	defaultPingrate = time.Second
)

func main() {
	// maxRuntime := flag.Duration()
	pingrate := flag.Duration("T", defaultPingrate, "Ping period. Delay between successive pings.")
	stopOnErr := flag.Bool("stoperr", true, "Stop on ping error or no response.")
	keepalive := flag.Duration("keepalive", defaultPingrate*10, "MQTT keepalive")
	wsurl := flag.String("url", "", "Websocket URL of server [Required]")
	user := flag.String("u", "", "MQTT username for server access")
	pass := flag.String("pass", "", "MQTT password for server access")
	clientID := flag.String("id", "", "MQTT Client ID. Must be unique among clients accessing server. If no argument is passed an ID is automatically generated.")
	loopbackTopic := flag.String("lbtest", "", "Loopback test of publish/subscribe payload.")
	flag.Parse()
	if *wsurl == "" {
		log.Fatal("Websocket URL not set")
	}
	if *clientID == "" {
		seed := uint64(time.Now().Unix()) % 1000
		*clientID = "pinger" + strconv.FormatUint(seed, 10)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := wsocket.NewClient(*clientID, wsocket.ClientConfig{
		URL:           *wsurl,
		MQTTKeepAlive: uint16((*keepalive).Seconds() + 1e-6), // +1e-6 to round up.
		Username:      *user,
		Password:      *pass,
		Subs:          mqtt.SubscriptionsMap{},
	})
	if err != nil {
		log.Fatal(err)
	}
	err = c.Connect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("connection success")
	if *loopbackTopic == "" {
		pingLoop(context.Background(), c, *pingrate, *keepalive, *stopOnErr)
	} else {
		loopbackLoop(context.Background(), c, *loopbackTopic, *pingrate, *keepalive, *stopOnErr)
	}

	_ = loopbackTopic
}

func pingLoop(mainCtx context.Context, c *wsocket.Client, pingrate, keepalive time.Duration, stopOnErr bool) {
	cancel := func() {}
	lastSuccess := time.Now()
	ctx := mainCtx
	for {
		time.Sleep(pingrate)
		err := c.Ping(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			log.Fatal("keepalive timeout exceeded, connection lost")
		}
		if stopOnErr && err != nil {
			log.Fatal("ping failed:", err)
		} else if err != nil {
			log.Println("ping failed:", err)
		} else {
			log.Printf("Ping OK (%s)", time.Since(lastSuccess))
			lastSuccess = time.Now()
			cancel()
			ctx, cancel = context.WithDeadline(mainCtx, lastSuccess.Add(keepalive))
		}
	}
}

func loopbackLoop(mainCtx context.Context, c *wsocket.Client, topic string, pingrate, keepalive time.Duration, stopOnErr bool) {
	payload := make([]byte, 2)
	err := c.Subscribe(mainCtx, []mqtt.SubscribeRequest{
		{TopicFilter: []byte(topic), QoS: mqtt.QoS0},
	})
	if err != nil {
		log.Fatalf("subscribe to %s failed: %s", topic, err)
	}
	cancel := func() {}
	lastSuccess := time.Now()
	ctx := mainCtx
	rxtx := c.UnsafeRxTx()
	for {
		currentPayload := uint16(lastSuccess.UnixMilli())
		// var currentPayload uint16 = 0x6446
		binary.BigEndian.PutUint16(payload, currentPayload)
		time.Sleep(pingrate)
		err := c.PublishPayload(ctx, topic, mqtt.QoS0, payload)
		rxtx.RxCallbacks.OnPub = func(rx *mqtt.Rx, vp mqtt.VariablesPublish, r io.Reader) error {
			pl, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			if len(pl) != 2 {
				return fmt.Errorf("expected to receive payload %x, got length %d: %x(%q)", currentPayload, len(pl), pl, pl)
			}
			received := binary.BigEndian.Uint16(pl)
			if received != currentPayload {
				return fmt.Errorf("expected to receive payload %x, got %x", currentPayload, received)
			}
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			log.Fatal("keepalive timeout exceeded, connection lost")
		}
		if stopOnErr && err != nil {
			log.Fatal("Publish loopback failed:", err)
		} else if err != nil {
			log.Println("Publish loopback failed:", err)
		} else {
		redo:
			_, err := rxtx.ReadNextPacket()
			if rxtx.LastReceivedHeader.Type() != mqtt.PacketPublish {
				log.Printf("unexpected %s packet, retrying", rxtx.LastReceivedHeader.Type())
				goto redo
			}
			if err != nil {
				log.Fatal("error during decoding:", err)
			}
			log.Printf("Publish loopback payload 0x%x OK (%s)", payload, time.Since(lastSuccess))
			lastSuccess = time.Now()
			cancel()
			ctx, cancel = context.WithDeadline(mainCtx, lastSuccess.Add(keepalive))
		}
	}
}
