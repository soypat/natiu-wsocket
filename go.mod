module github.com/soypat/natiu-wsocket

go 1.19

replace github.com/soypat/peasocket => ../peasocket
require (
	github.com/soypat/natiu-mqtt v0.3.1
	github.com/soypat/peasocket v0.0.0-20230115220831-a52d78cbcc2f
	nhooyr.io/websocket v1.8.7
)

require github.com/klauspost/compress v1.10.3 // indirect
