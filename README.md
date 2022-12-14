# wsmqtt
MQTT websocket implementation using [natiu-mqtt](https://github.com/soypat/natiu-mqtt).

## Simplicity
It took 255 lines of code to get the first `cmd/mqttping` up and working.
The wsocket package itself was 171 lines at that point.

## `cmd/mqttping`
Program for server discovery and uptime checker. See [`mqttping.go`](./cmd/mqttping/mqttping.go).

Example output shown below. Network cable unplugged after 6 seconds of runtime.
```
mqttping -url=ws://dashboard.ci/qa -u=username -pass=123 -keepalive=10s
2022/12/14 15:20:18 connection success
2022/12/14 15:20:19 Ping OK (1.003365536s)
2022/12/14 15:20:20 Ping OK (1.003063079s)
2022/12/14 15:20:21 Ping OK (1.004336657s)
2022/12/14 15:20:22 Ping OK (1.003325537s)
2022/12/14 15:20:23 Ping OK (1.003230131s)
2022/12/14 15:20:33 ping failed:failed to get reader: received continuation frame without text or binary frame
```