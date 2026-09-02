package handler

import (
	"net/http"
	"strconv"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"os"
	"strings"
)

const maxTerminalInputBytes = 64 * 1024

var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 * 1024,
	WriteBufferSize: 16 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || origin == "null" ||
			strings.HasPrefix(origin, "lingcowork://") ||
			origin == "http://localhost:5173" ||
			origin == "http://127.0.0.1:5173"
	},
}

type terminalControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func terminalEnvironment() []string {
	env := append([]string(nil), os.Environ()...)
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	// The terminal stream is decoded as UTF-8 by xterm.js, so ask the usual
	// runtimes to emit UTF-8 rather than the legacy Windows codepage. Harmless
	// on Unix, where UTF-8 is already the default.
	env = append(env,
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	)
	return env
}

func queryInt(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return fallback
	}
	return value
}

func clampTerminalDimension(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
