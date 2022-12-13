module github.com/soypat/wsmqtt

go 1.19

replace github.com/soypat/natiu-mqtt => ../natiu-mqtt

require (
	github.com/soypat/natiu-mqtt v0.0.0-20221212210843-650ed5092d48
	nhooyr.io/websocket v1.8.7
)

require github.com/klauspost/compress v1.10.3 // indirect
