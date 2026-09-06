// Package camera speaks the XiongMai / XM "DVRIP" (Sofia) protocol on TCP 34567,
// enough to read and change encode/image settings, reboot, and drive PTZ.
package camera

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	msgLogin      = 1000
	msgKeepAlive  = 1006
	msgSetInfo    = 1040
	msgGetInfo    = 1042
	msgSystemInfo = 1020
	msgOPMachine  = 1450
	msgPTZ        = 1400
)

// Client is a DVRIP connection. Safe for concurrent use.
type Client struct {
	host, user, pass string

	mu      sync.Mutex
	conn    net.Conn
	session uint32
	seq     uint32
	alive   time.Duration
	stop    chan struct{}
}

func New(host, user, pass string) *Client {
	return &Client{host: host, user: user, pass: pass}
}

// sofiaHash is XM's password digest: md5, then 8 chars from a 62-char alphabet.
func sofiaHash(pw string) string {
	sum := md5.Sum([]byte(pw))
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[i] = chars[(int(sum[2*i])+int(sum[2*i+1]))%62]
	}
	return string(out)
}

func (c *Client) writePacket(msg uint16, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body = append(body, 0x0a, 0x00)

	hdr := make([]byte, 20)
	hdr[0] = 0xff
	binary.LittleEndian.PutUint32(hdr[4:], c.session)
	binary.LittleEndian.PutUint32(hdr[8:], c.seq)
	binary.LittleEndian.PutUint16(hdr[14:], msg)
	binary.LittleEndian.PutUint32(hdr[16:], uint32(len(body)))
	c.seq++

	_ = c.conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	if _, err := c.conn.Write(append(hdr, body...)); err != nil {
		return err
	}
	return nil
}

func (c *Client) readPacket() (map[string]json.RawMessage, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	hdr := make([]byte, 20)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return nil, err
	}
	c.session = binary.LittleEndian.Uint32(hdr[4:])
	n := binary.LittleEndian.Uint32(hdr[16:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.conn, buf); err != nil {
		return nil, err
	}
	buf = bytes.TrimRight(buf, "\x00\x0a")
	var out map[string]json.RawMessage
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, fmt.Errorf("decode reply: %w (%s)", err, buf)
	}
	return out, nil
}

func (c *Client) roundtrip(msg uint16, payload any) (map[string]json.RawMessage, error) {
	if err := c.writePacket(msg, payload); err != nil {
		return nil, err
	}
	return c.readPacket()
}

func sessionHex(s uint32) string { return fmt.Sprintf("0x%08X", s) }

func retOK(m map[string]json.RawMessage) error {
	var ret int
	if raw, ok := m["Ret"]; ok {
		_ = json.Unmarshal(raw, &ret)
	}
	if ret == 100 || ret == 515 {
		return nil
	}
	return fmt.Errorf("camera returned Ret=%d", ret)
}

// Connect logs in and starts the keep-alive loop.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.host, "34567"), 8*time.Second)
	if err != nil {
		return err
	}
	c.conn = conn
	c.session = 0
	c.seq = 0

	reply, err := c.roundtrip(msgLogin, map[string]string{
		"EncryptType": "MD5",
		"LoginType":   "DVRIP-Web",
		"UserName":    c.user,
		"PassWord":    sofiaHash(c.pass),
	})
	if err != nil {
		c.closeLocked()
		return err
	}
	if err := retOK(reply); err != nil {
		c.closeLocked()
		return fmt.Errorf("login: %w", err)
	}
	var sid string
	_ = json.Unmarshal(reply["SessionID"], &sid)
	fmt.Sscanf(sid, "0x%x", &c.session)
	var ai int
	if raw, ok := reply["AliveInterval"]; ok {
		_ = json.Unmarshal(raw, &ai)
	}
	if ai <= 0 {
		ai = 20
	}
	c.alive = time.Duration(ai) * time.Second
	c.stop = make(chan struct{})
	go c.keepAliveLoop(c.stop)
	return nil
}

func (c *Client) keepAliveLoop(stop chan struct{}) {
	t := time.NewTicker(c.alive)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.mu.Lock()
			if c.conn == nil {
				c.mu.Unlock()
				return
			}
			_, err := c.roundtrip(msgKeepAlive, map[string]string{
				"Name":      "KeepAlive",
				"SessionID": fmt.Sprintf("0x%08X", c.session),
			})
			c.mu.Unlock()
			if err != nil {
				c.Close()
				return
			}
		}
	}
}

func (c *Client) closeLocked() {
	if c.stop != nil {
		select {
		case <-c.stop:
		default:
			close(c.stop)
		}
		c.stop = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
}

func (c *Client) ensure() error {
	c.mu.Lock()
	ok := c.conn != nil
	c.mu.Unlock()
	if ok {
		return nil
	}
	return c.Connect()
}

// GetInfo reads a config node (e.g. "Simplify.Encode", "Camera.Param",
// "SystemInfo") and returns its raw JSON value.
func (c *Client) GetInfo(name string) (json.RawMessage, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.roundtrip(msgGetInfo, map[string]string{
		"Name":      name,
		"SessionID": fmt.Sprintf("0x%08X", c.session),
	})
	if err != nil {
		c.closeLocked()
		return nil, err
	}
	if v, ok := reply[name]; ok {
		return v, nil
	}
	return nil, retOK(reply)
}

// SetInfo writes a config node. value is marshalled as-is.
func (c *Client) SetInfo(name string, value any) error {
	if err := c.ensure(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.roundtrip(msgSetInfo, map[string]any{
		"Name":      name,
		"SessionID": fmt.Sprintf("0x%08X", c.session),
		name:        value,
	})
	if err != nil {
		c.closeLocked()
		return err
	}
	return retOK(reply)
}

// Reboot asks the camera to restart.
func (c *Client) Reboot() error {
	if err := c.ensure(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.roundtrip(msgOPMachine, map[string]any{
		"Name":      "OPMachine",
		"SessionID": fmt.Sprintf("0x%08X", c.session),
		"OPMachine": map[string]string{"Action": "Reboot"},
	})
	c.closeLocked()
	return err
}

// PTZ sends one pan/tilt step. cmd is an XM direction verb: "DirectionUp",
// "DirectionDown", "DirectionLeft", "DirectionRight" (also the diagonals /
// "ZoomTile" / "ZoomWide" / "FocusNear" / "FocusFar"). step is 1..8 (speed).
//
// The camera moves one increment per call and auto-stops — there is no separate
// stop command. For continuous motion the caller repeats this while the button
// is held. Payload shape matches the XM CMS app exactly (Pattern "SetBegin",
// Preset 65535, POINT block), which the plain "Start" form does not.
func (c *Client) PTZ(cmd string, step int) error {
	if err := c.ensure(); err != nil {
		return err
	}
	if step <= 0 {
		step = 5
	}
	if step > 8 {
		step = 8
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.roundtrip(msgPTZ, map[string]any{
		"Name":      "OPPTZControl",
		"SessionID": fmt.Sprintf("0x%02X", c.session),
		"OPPTZControl": map[string]any{
			"Command": cmd,
			"Parameter": map[string]any{
				"AUX":      map[string]any{"Number": 0, "Status": "On"},
				"Channel":  0,
				"MenuOpts": "Enter",
				"POINT":    map[string]any{"bottom": 0, "left": 0, "right": 0, "top": 0},
				"Pattern":  "SetBegin",
				"Preset":   65535,
				"Step":     step,
				"Tour":     0,
			},
		},
	})
	if err != nil {
		c.closeLocked()
		return err
	}
	return retOK(reply)
}
