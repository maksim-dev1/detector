// detect-image runs the YOLO pipeline on a single image file — for validating
// the model/bindings and tuning conf_threshold without a live camera.
//
//	go run ./cmd/detect-image -conf 0.25 bus.jpg
package main

import (
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"log"
	"os"

	xdraw "golang.org/x/image/draw"

	"github.com/maksim-dev1/detector/internal/detect"
	"github.com/maksim-dev1/detector/internal/media"
)

func main() {
	yoloURL := flag.String("yolo", "http://192.168.0.155:8000", "YOLO service base URL")
	size := flag.Int("size", 640, "model input size")
	conf := flag.Float64("conf", 0.25, "confidence threshold")
	iou := flag.Float64("iou", 0.45, "NMS IoU threshold")
	outPath := flag.String("out", "", "optional annotated jpeg output path")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: detect-image [flags] <image>")
	}

	src, err := loadRGBA(flag.Arg(0))
	if err != nil {
		log.Fatalf("load image: %v", err)
	}
	b := src.Bounds()
	lb := media.NewLetterbox(media.Size{W: b.Dx(), H: b.Dy()}, *size)

	// letterbox into an SxS canvas
	canvas := image.NewRGBA(image.Rect(0, 0, *size, *size))
	dst := image.Rect(lb.PadX, lb.PadY, lb.PadX+lb.NewW, lb.PadY+lb.NewH)
	xdraw.ApproxBiLinear.Scale(canvas, dst, src, b, draw.Over, nil)

	rgb := make([]byte, *size**size*3)
	for i := 0; i < *size**size; i++ {
		rgb[i*3+0] = canvas.Pix[i*4+0]
		rgb[i*3+1] = canvas.Pix[i*4+1]
		rgb[i*3+2] = canvas.Pix[i*4+2]
	}

	y, err := detect.New(detect.Config{
		YoloURL:       *yoloURL,
		InputSize:     *size,
		ConfThreshold: float32(*conf),
		IOUThreshold:  float32(*iou),
	})
	if err != nil {
		log.Fatalf("detector: %v", err)
	}
	defer y.Close()

	boxes, err := y.Detect(rgb)
	if err != nil {
		log.Fatalf("detect: %v", err)
	}
	fmt.Printf("%d detections:\n", len(boxes))
	for _, d := range boxes {
		nx1, ny1, nx2, ny2 := lb.ToSource(d.X1, d.Y1, d.X2, d.Y2)
		fmt.Printf("  %-14s %.2f  src[%.3f,%.3f,%.3f,%.3f]\n", d.Label, d.Score, nx1, ny1, nx2, ny2)
	}

	if *outPath != "" {
		detect.Annotate(canvas, boxes)
		f, err := os.Create(*outPath)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := jpeg.Encode(f, canvas, &jpeg.Options{Quality: 90}); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", *outPath)
	}
}

func loadRGBA(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	return rgba, nil
}
