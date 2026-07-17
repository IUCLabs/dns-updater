package config

import (
	"net"
	"os"
)

// SdNotify sends a state string to systemd via NOTIFY_SOCKET (Type=notify). It
// is a no-op when not run under systemd. Implemented inline to avoid a
// dependency for a few lines of socket writing.
func SdNotify(state string) {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return
	}
	addr := &net.UnixAddr{Name: sock, Net: "unixgram"}
	// Abstract namespace sockets start with '@'.
	if len(sock) > 0 && sock[0] == '@' {
		addr.Name = "\x00" + sock[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(state))
}
