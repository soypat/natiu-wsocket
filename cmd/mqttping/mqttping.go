package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"strconv"
	"time"

	wsocket "github.com/soypat/natiu-wsocket"
)

const (
	defaultPingrate = time.Second
)

func main() {
	pingrate := flag.Duration("T", defaultPingrate, "Ping period. Delay between successive pings.")
	stopOnErr := flag.Bool("stoperr", true, "Stop on ping error or no response.")
	keepalive := flag.Duration("keepalive", defaultPingrate*10, "MQTT keepalive")
	wsurl := flag.String("url", "", "Websocket URL of server [Required]")
	user := flag.String("u", "", "MQTT username for server access")
	pass := flag.String("pass", "", "MQTT password for server access")
	clientID := flag.String("id", "", "MQTT Client ID. Must be unique among clients accessing server. If no argument is passed an ID is automatically generated.")
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
	})
	if err != nil {
		log.Fatal(err)
	}
	err = c.Connect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	lastSuccess := time.Now()
	log.Println("connection success")
	for {
		time.Sleep(*pingrate)
		err := c.Ping(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			log.Fatal("keepalive timeout exceeded, connection lost")
		}
		if *stopOnErr && err != nil {
			log.Fatal("ping failed:", err)
		} else if err != nil {
			log.Println("ping failed:", err)
		} else {
			log.Printf("Ping OK (%s)", time.Since(lastSuccess))
			lastSuccess = time.Now()
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), lastSuccess.Add(*keepalive))
		}
	}
}
