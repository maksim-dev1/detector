package detect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config configures the remote YOLO detector.
type Config struct {
	YoloURL       string  // base URL of the YOLO HTTP service, e.g. http://192.168.0.155:8000
	InputSize     int     // square frame size the pipeline feeds Detect
	ConfThreshold float32 // confidence floor
	IOUThreshold  float32 // kept for API compatibility; NMS is done service-side
	Classes       []string
	HTTPTimeout   time.Duration
}

// YOLO talks to an external YOLO inference service (POST /detect, multipart
// image upload -> JSON boxes). It keeps the same surface the pipeline used for
// the old in-process onnxruntime detector.
type YOLO struct {
	mu     sync.Mutex
	url    string
	client *http.Client
	size   int
	conf   float32
	want   map[int]bool
}

type detResponse struct {
	Detections []struct {
		Class      string    `json:"class"`
		Confidence float32   `json:"confidence"`
		BBox       []float32 `json:"bbox"` // [x1,y1,x2,y2] in sent-image pixels
	} `json:"detections"`
	Count int `json:"count"`
}

// New prepares a client for the YOLO service. It does not dial until Detect.
func New(cfg Config) (*YOLO, error) {
	s := cfg.InputSize
	if s == 0 {
		s = 640
	}
	base := strings.TrimRight(cfg.YoloURL, "/")
	if base == "" {
		return nil, fmt.Errorf("detect: yolo_url is required")
	}
	to := cfg.HTTPTimeout
	if to == 0 {
		to = 5 * time.Second
	}

	var want map[int]bool
	if len(cfg.Classes) > 0 {
		want = make(map[int]bool)
		for _, name := range cfg.Classes {
			idx := ClassIndex(name)
			if idx < 0 {
				return nil, fmt.Errorf("unknown COCO class %q", name)
			}
			want[idx] = true
		}
	}

	return &YOLO{
		url:    base,
		client: &http.Client{Timeout: to},
		size:   s,
		conf:   cfg.ConfThreshold,
		want:   want,
	}, nil
}

// Detect runs inference on one packed RGB frame (len size*size*3) and returns
// filtered boxes in frame pixel space.
func (y *YOLO) Detect(rgb []byte) ([]Box, error) {
	y.mu.Lock()
	url := y.url
	size := y.size
	conf := y.conf
	want := y.want
	y.mu.Unlock()

	if len(rgb) != size*size*3 {
		return nil, fmt.Errorf("frame size mismatch: got %d want %d", len(rgb), size*size*3)
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < size*size; i++ {
		img.Pix[i*4+0] = rgb[i*3+0]
		img.Pix[i*4+1] = rgb[i*3+1]
		img.Pix[i*4+2] = rgb[i*3+2]
		img.Pix[i*4+3] = 255
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "frame.jpg")
	if err != nil {
		return nil, err
	}
	if err := jpeg.Encode(fw, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	mw.Close()

	endpoint := url + "/detect?confidence=" + strconv.FormatFloat(float64(conf), 'f', 3, 32)
	req, err := http.NewRequest(http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yolo request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yolo %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var dr detResponse
	if err := json.Unmarshal(raw, &dr); err != nil {
		return nil, fmt.Errorf("yolo decode: %w", err)
	}

	boxes := make([]Box, 0, len(dr.Detections))
	for _, d := range dr.Detections {
		if d.Confidence < conf {
			continue
		}
		idx := ClassIndex(d.Class)
		if idx < 0 {
			continue
		}
		if want != nil && !want[idx] {
			continue
		}
		if len(d.BBox) != 4 {
			continue
		}
		boxes = append(boxes, Box{
			X1: d.BBox[0], Y1: d.BBox[1], X2: d.BBox[2], Y2: d.BBox[3],
			Score: d.Confidence, Class: idx, Label: COCONames[idx],
		})
	}
	return boxes, nil
}

// InputSize returns the square frame dimension the detector expects.
func (y *YOLO) InputSize() int { return y.size }

// SetConf changes the confidence threshold at runtime.
func (y *YOLO) SetConf(v float32) {
	y.mu.Lock()
	y.conf = v
	y.mu.Unlock()
}

// SetClasses changes the wanted-class filter at runtime. Empty = all classes.
// Unknown names are ignored.
func (y *YOLO) SetClasses(names []string) {
	var want map[int]bool
	if len(names) > 0 {
		want = make(map[int]bool)
		for _, n := range names {
			if idx := ClassIndex(n); idx >= 0 {
				want[idx] = true
			}
		}
	}
	y.mu.Lock()
	y.want = want
	y.mu.Unlock()
}

// Close is a no-op; kept for API compatibility.
func (y *YOLO) Close() {}
