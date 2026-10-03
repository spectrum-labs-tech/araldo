// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// Video (ADR 0027): MP4 and QuickTime files, read from their index (the
// moov box of the ISO base media file format, ISO/IEC 14496-12) without
// decoding a frame.

// Video types Araldo accepts.
const (
	MP4       = "video/mp4"
	QuickTime = "video/quicktime"
)

// VideoTypes lists the accepted video types.
var VideoTypes = []string{MP4, QuickTime}

// IsVideo reports whether a type is a video type.
func IsVideo(typ string) bool { return typ == MP4 || typ == QuickTime }

// maxIndex bounds the moov box Araldo reads; a long video's index is a few
// megabytes.
const maxIndex = 64 << 20

// ErrNotVideo is returned for anything but an MP4 or QuickTime file.
var ErrNotVideo = errors.New("not an MP4 or QuickTime video")

// Video is what Araldo knows about a video.
type Video struct {
	Type string
	// Width and Height are as shown, after the rotation phones record.
	Width, Height int
	Duration      time.Duration
	FrameRate     float64
	// VideoCodec and AudioCodec are the sample entries' four-character
	// codes ("avc1" for H.264, "hvc1" for HEVC, "mp4a" for AAC); AudioCodec
	// is empty for a silent video.
	VideoCodec string
	AudioCodec string
}

// SniffVideo reports a file's video type from its first bytes: an ftyp box,
// whose brand "qt  " marks QuickTime.
func SniffVideo(head []byte) (string, bool) {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return "", false
	}
	if string(head[8:12]) == "qt  " {
		return QuickTime, true
	}
	return MP4, true
}

// ProbeVideo reads a video's index. r is read in small ranges, so it can be
// an object in storage.
func ProbeVideo(r io.ReaderAt, size int64) (Video, error) {
	head := make([]byte, 12)
	if _, err := r.ReadAt(head, 0); err != nil {
		return Video{}, ErrNotVideo
	}
	typ, ok := SniffVideo(head)
	if !ok {
		return Video{}, ErrNotVideo
	}
	moov, err := findBox(r, size, "moov")
	if err != nil {
		return Video{}, err
	}
	v := Video{Type: typ}
	if err := v.readMoov(moov); err != nil {
		return Video{}, err
	}
	if v.VideoCodec == "" || v.Width <= 0 || v.Height <= 0 {
		return Video{}, errors.New("the file has no video track")
	}
	return v, nil
}

// box is a box's type and payload bounds within its parent.
type box struct {
	typ        string
	start, end int64 // payload
}

// header reads a box header at off: its type and payload bounds.
func header(r io.ReaderAt, off, limit int64) (box, error) {
	var h [16]byte
	if _, err := r.ReadAt(h[:8], off); err != nil {
		return box{}, err
	}
	size := int64(binary.BigEndian.Uint32(h[:4]))
	b := box{typ: string(h[4:8]), start: off + 8}
	switch size {
	case 0: // to the end
		size = limit - off
	case 1: // a 64-bit size follows
		if _, err := r.ReadAt(h[8:16], off+8); err != nil {
			return box{}, err
		}
		large := binary.BigEndian.Uint64(h[8:16])
		if large > math.MaxInt64 {
			return box{}, fmt.Errorf("a %q box runs past the file", b.typ)
		}
		size, b.start = int64(large), off+16 //nolint:gosec // G115: bounded on the line above
	}
	if size < b.start-off || off+size > limit {
		return box{}, fmt.Errorf("a %q box runs past the file", b.typ)
	}
	b.end = off + size
	return b, nil
}

// findBox reads a top-level box's payload.
func findBox(r io.ReaderAt, size int64, typ string) ([]byte, error) {
	for off := int64(0); off < size; {
		b, err := header(r, off, size)
		if err != nil {
			return nil, err
		}
		if b.typ == typ {
			n := b.end - b.start
			if n > maxIndex {
				return nil, fmt.Errorf("the %q box is %d bytes, more than Araldo reads", typ, n)
			}
			buf := make([]byte, n)
			if _, err := r.ReadAt(buf, b.start); err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return buf, nil
		}
		off = b.end
	}
	return nil, fmt.Errorf("no %q box: not a complete video", typ)
}

// children lists the boxes in a payload.
func children(p []byte) []box {
	var out []box
	r := bytes.NewReader(p)
	for off := int64(0); off+8 <= int64(len(p)); {
		b, err := header(r, off, int64(len(p)))
		if err != nil {
			return out
		}
		out = append(out, b)
		off = b.end
	}
	return out
}

func child(p []byte, path ...string) []byte {
	for _, name := range path {
		found := false
		for _, b := range children(p) {
			if b.typ == name {
				p, found = p[b.start:b.end], true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return p
}

func (v *Video) readMoov(moov []byte) error {
	if mvhd := child(moov, "mvhd"); len(mvhd) >= 20 {
		var scale, dur uint64
		if mvhd[0] == 1 && len(mvhd) >= 32 {
			scale, dur = uint64(binary.BigEndian.Uint32(mvhd[20:24])), binary.BigEndian.Uint64(mvhd[24:32])
		} else {
			scale, dur = uint64(binary.BigEndian.Uint32(mvhd[12:16])), uint64(binary.BigEndian.Uint32(mvhd[16:20]))
		}
		if scale > 0 {
			v.Duration = time.Duration(float64(dur) / float64(scale) * float64(time.Second))
		}
	}
	for _, b := range children(moov) {
		if b.typ != "trak" {
			continue
		}
		trak := moov[b.start:b.end]
		hdlr := child(trak, "mdia", "hdlr")
		if len(hdlr) < 12 {
			continue
		}
		codec := ""
		if stsd := child(trak, "mdia", "minf", "stbl", "stsd"); len(stsd) >= 16 {
			codec = string(stsd[12:16])
		}
		switch string(hdlr[8:12]) {
		case "vide":
			if v.VideoCodec != "" {
				continue // the first video track is the video
			}
			v.VideoCodec = codec
			v.readTkhd(child(trak, "tkhd"))
			v.FrameRate = frameRate(child(trak, "mdia", "mdhd"), child(trak, "mdia", "minf", "stbl", "stts"))
		case "soun":
			if v.AudioCodec == "" {
				v.AudioCodec = codec
			}
		}
	}
	return nil
}

// readTkhd reads a track's display size (16.16 fixed point) and its
// transformation matrix: a quarter turn swaps width and height.
func (v *Video) readTkhd(t []byte) {
	off := 76 // version 0: header 4, times 8, id 4, reserved 4, duration 4, reserved 8, layer etc 8, matrix 36
	if len(t) > 0 && t[0] == 1 {
		off = 88 // 64-bit times and duration
	}
	if len(t) < off+8 {
		return
	}
	matrix := t[off-36 : off]
	w := int(binary.BigEndian.Uint32(t[off:off+4]) >> 16)
	h := int(binary.BigEndian.Uint32(t[off+4:off+8]) >> 16)
	// The matrix's first row: a quarter turn has no a and a nonzero b.
	a := binary.BigEndian.Uint32(matrix[0:4])
	b := binary.BigEndian.Uint32(matrix[4:8])
	if a == 0 && b != 0 {
		w, h = h, w
	}
	v.Width, v.Height = w, h
}

// frameRate is a track's samples per second, from its media header's
// timescale and duration and its sample-to-time table.
func frameRate(mdhd, stts []byte) float64 {
	if len(mdhd) < 20 || len(stts) < 8 {
		return 0
	}
	var scale, dur uint64
	if mdhd[0] == 1 && len(mdhd) >= 32 {
		scale, dur = uint64(binary.BigEndian.Uint32(mdhd[20:24])), binary.BigEndian.Uint64(mdhd[24:32])
	} else {
		scale, dur = uint64(binary.BigEndian.Uint32(mdhd[12:16])), uint64(binary.BigEndian.Uint32(mdhd[16:20]))
	}
	if scale == 0 || dur == 0 {
		return 0
	}
	entries := int(binary.BigEndian.Uint32(stts[4:8]))
	var samples uint64
	for i := 0; i < entries && 8+8*i+8 <= len(stts); i++ {
		samples += uint64(binary.BigEndian.Uint32(stts[8+8*i : 12+8*i]))
	}
	fps := float64(samples) / (float64(dur) / float64(scale))
	return float64(int(fps*100+0.5)) / 100
}
