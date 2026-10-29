package daemon

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// NotifyEnv names the socket systemd's Type=notify services report on.
const NotifyEnv = "NOTIFY_SOCKET"

const notifyTimeout = 5 * time.Second

// Notify sends state, such as "READY=1", to the notify socket at addr, as
// sd_notify does; "@" leads an abstract address. With addr empty there is no
// supervisor listening and Notify does nothing.
func Notify(addr, state string) error {
	if addr == "" {
		return nil
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("daemon: reach the notify socket %s: %w", addr, err)
	}
	err = send(c, state)
	if cerr := c.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("daemon: close the notify socket %s: %w", addr, cerr))
	}
	if err != nil {
		return fmt.Errorf("daemon: notify %s of %q: %w", addr, state, err)
	}
	return nil
}

func send(c *net.UnixConn, state string) error {
	if err := c.SetWriteDeadline(time.Now().Add(notifyTimeout)); err != nil {
		return err
	}
	_, err := c.Write([]byte(state))
	return err
}
