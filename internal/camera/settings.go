package camera

import "encoding/json"

// EncodeSettings is the subset of Simplify.Encode the UI exposes.
type EncodeSettings struct {
	MainFPS        int    `json:"main_fps"`
	MainBitRate    int    `json:"main_bitrate"`
	MainResolution string `json:"main_resolution"`
	MainCodec      string `json:"main_codec"`
	SubFPS         int    `json:"sub_fps"`
	SubBitRate     int    `json:"sub_bitrate"`
}

// EncodePatch carries optional changes; nil fields are left untouched.
type EncodePatch struct {
	MainFPS     *int `json:"main_fps"`
	MainBitRate *int `json:"main_bitrate"`
	SubFPS      *int `json:"sub_fps"`
	SubBitRate  *int `json:"sub_bitrate"`
}

func (c *Client) GetEncode() (EncodeSettings, error) {
	raw, err := c.GetInfo("Simplify.Encode")
	if err != nil {
		return EncodeSettings{}, err
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return EncodeSettings{}, err
	}
	var s EncodeSettings
	if v := videoNode(arr[0], "MainFormat"); v != nil {
		s.MainFPS = intOf(v["FPS"])
		s.MainBitRate = intOf(v["BitRate"])
		s.MainResolution = strOf(v["Resolution"])
		s.MainCodec = strOf(v["Compression"])
	}
	if v := videoNode(arr[0], "ExtraFormat"); v != nil {
		s.SubFPS = intOf(v["FPS"])
		s.SubBitRate = intOf(v["BitRate"])
	}
	return s, nil
}

// ApplyEncode does a read-modify-write of Simplify.Encode with the patch.
func (c *Client) ApplyEncode(p EncodePatch) error {
	raw, err := c.GetInfo("Simplify.Encode")
	if err != nil {
		return err
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return err
	}
	setV := func(fmtName string, fps, br *int) {
		f, _ := arr[0][fmtName].(map[string]any)
		if f == nil {
			return
		}
		v, _ := f["Video"].(map[string]any)
		if v == nil {
			return
		}
		if fps != nil {
			v["FPS"] = *fps
		}
		if br != nil {
			v["BitRate"] = *br
		}
	}
	setV("MainFormat", p.MainFPS, p.MainBitRate)
	setV("ExtraFormat", p.SubFPS, p.SubBitRate)
	return c.SetInfo("Simplify.Encode", arr)
}

// DayNight sets Camera.Param.DayNightColor: "auto" | "color" | "bw".
func (c *Client) DayNight(mode string) error {
	raw, err := c.GetInfo("Camera.Param")
	if err != nil {
		return err
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return err
	}
	val := "0x00000000" // auto
	switch mode {
	case "color":
		val = "0x00000001"
	case "bw":
		val = "0x00000002"
	}
	arr[0]["DayNightColor"] = val
	return c.SetInfo("Camera.Param", arr)
}

// WhiteLight controls the camera's built-in floodlight (Camera.WhiteLight).
// mode: "auto" | "on" | "off". brightness 0..100 (used for "on").
func (c *Client) WhiteLight(mode string, brightness int) error {
	raw, err := c.GetInfo("Camera.WhiteLight")
	if err != nil {
		return err
	}
	var wl map[string]any
	if err := json.Unmarshal(raw, &wl); err != nil {
		return err
	}
	switch mode {
	case "on":
		wl["WorkMode"] = "Manual"
	case "off":
		wl["WorkMode"] = "Close"
	default:
		wl["WorkMode"] = "Auto"
	}
	if brightness > 0 {
		if brightness > 100 {
			brightness = 100
		}
		wl["Brightness"] = brightness
	}
	// force-on should not be gated by the day/night schedule
	if wp, ok := wl["WorkPeriod"].(map[string]any); ok {
		if mode == "on" {
			wp["Enable"] = 0
		} else {
			wp["Enable"] = 1
		}
	}
	return c.SetInfo("Camera.WhiteLight", wl)
}

// SystemInfo returns the raw SystemInfo node (model, firmware, serial…).
func (c *Client) SystemInfo() (json.RawMessage, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.roundtrip(msgSystemInfo, map[string]string{
		"Name":      "SystemInfo",
		"SessionID": sessionHex(c.session),
	})
	if err != nil {
		c.closeLocked()
		return nil, err
	}
	if v, ok := reply["SystemInfo"]; ok {
		return v, nil
	}
	return nil, retOK(reply)
}

func videoNode(m map[string]json.RawMessage, fmtName string) map[string]json.RawMessage {
	raw, ok := m[fmtName]
	if !ok {
		return nil
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	vraw, ok := f["Video"]
	if !ok {
		return nil
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(vraw, &v) != nil {
		return nil
	}
	return v
}

func intOf(r json.RawMessage) int {
	var i int
	_ = json.Unmarshal(r, &i)
	return i
}

func strOf(r json.RawMessage) string {
	var s string
	_ = json.Unmarshal(r, &s)
	return s
}
