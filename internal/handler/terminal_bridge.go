package handler

import (
	"encoding/json"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/manager"
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

// bridgeManagedTerminalWebSocket 把浏览器 WebSocket 接到会话注册表里的持久 PTY。
//
// 与 bridgeTerminalWebSocket 的区别：
//   - 输出来自订阅者通道（支持多订阅者 / 断线重放）；
//   - 除二进制输出外，还会把 session / exec / exit 等 JSON 控制事件以文本帧下发，
//     前端据此打印「[agent] $ cmd」之类的提示。
func bridgeManagedTerminalWebSocket(conn *websocket.Conn, session *manager.ManagedTerminalSession, sub *manager.TerminalSubscriber) {
	// 唯一的写协程：二进制 = PTY 输出，文本 = JSON 控制事件。
	go func() {
		for {
			select {
			case chunk := <-sub.Data:
				// 打印chunk的内容
				// fmt.Printf("Sending chunk to websocket: %s\n", string(chunk))
				if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
					return
				}
			case event := <-sub.Events:
				if err := conn.WriteMessage(websocket.TextMessage, event); err != nil {
					return
				}
			case <-sub.Done():
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
		if _, writeErr := session.WriteInput(data); writeErr != nil {
			return
		}
	}
}
