package handler

import (
	"encoding/json"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/gorilla/websocket"
)

// bridgeTerminalWebSocket copies bytes between a browser WebSocket and a runtime
// terminal until either side closes. It is the only writer to conn.
//
// Browser -> backend: binary/text frames are key input; a text frame
// {"type":"resize","rows":..,"cols":..} is a terminal size control frame.
// Backend -> browser: binary output bytes.
func bridgeTerminalWebSocket(conn *websocket.Conn, session containerruntime.TerminalSession) {
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := session.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	for {
		messageType, data, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		if messageType == websocket.TextMessage {
			var control struct {
				Type string `json:"type"`
				Rows uint16 `json:"rows"`
				Cols uint16 `json:"cols"`
			}
			if json.Unmarshal(data, &control) == nil && control.Type == "resize" {
				_ = session.Resize(control.Rows, control.Cols)
				continue
			}
		}
		if _, writeErr := session.Write(data); writeErr != nil {
			return
		}
	}
}
