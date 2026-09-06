package detect

import "sort"

// Box is an axis-aligned detection in model (letterboxed) pixel space.
type Box struct {
	X1, Y1, X2, Y2 float32
	Score          float32
	Class          int
	Label          string
}

func (b Box) area() float32 {
	w := b.X2 - b.X1
	h := b.Y2 - b.Y1
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

func iou(a, b Box) float32 {
	x1 := max32(a.X1, b.X1)
	y1 := max32(a.Y1, b.Y1)
	x2 := min32(a.X2, b.X2)
	y2 := min32(a.Y2, b.Y2)
	iw := x2 - x1
	ih := y2 - y1
	if iw <= 0 || ih <= 0 {
		return 0
	}
	inter := iw * ih
	return inter / (a.area() + b.area() - inter)
}

// decode parses a YOLOv8 output tensor of shape [1, 4+numClasses, numBoxes]
// (row-major) into candidate boxes above confThresh, keeping only wanted classes.
func decode(out []float32, numClasses, numBoxes int, confThresh float32, want map[int]bool) []Box {
	stride := numBoxes
	var boxes []Box
	for i := 0; i < numBoxes; i++ {
		bestC, bestS := -1, float32(0)
		for c := 0; c < numClasses; c++ {
			s := out[(4+c)*stride+i]
			if s > bestS {
				bestS, bestC = s, c
			}
		}
		if bestC < 0 || bestS < confThresh {
			continue
		}
		if want != nil && !want[bestC] {
			continue
		}
		cx := out[0*stride+i]
		cy := out[1*stride+i]
		w := out[2*stride+i]
		h := out[3*stride+i]
		boxes = append(boxes, Box{
			X1: cx - w/2, Y1: cy - h/2,
			X2: cx + w/2, Y2: cy + h/2,
			Score: bestS, Class: bestC, Label: COCONames[bestC],
		})
	}
	return boxes
}

// nms performs per-class non-maximum suppression.
func nms(boxes []Box, iouThresh float32) []Box {
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].Score > boxes[j].Score })
	var kept []Box
	suppressed := make([]bool, len(boxes))
	for i := range boxes {
		if suppressed[i] {
			continue
		}
		kept = append(kept, boxes[i])
		for j := i + 1; j < len(boxes); j++ {
			if suppressed[j] || boxes[j].Class != boxes[i].Class {
				continue
			}
			if iou(boxes[i], boxes[j]) > iouThresh {
				suppressed[j] = true
			}
		}
	}
	return kept
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
