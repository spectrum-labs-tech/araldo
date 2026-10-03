// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// mp4box builds a box: a 32-bit size, a type, then the payload.
func mp4box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// testVideo describes a synthetic video.
type testVideo struct {
	brand        string
	w, h         int
	rotate       bool // a quarter turn, as a phone held upright records
	seconds      int  // at timescale 1000
	frames       uint32
	audio        bool
	moovLast     bool
	largeMdat    bool
	tkhdVersion1 bool
}

func (v testVideo) bytes() []byte {
	mvhd := mp4box("mvhd", u32(0), u32(0), u32(0), u32(1000), u32(uint32(v.seconds*1000)), make([]byte, 80))
	matrix := []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	if v.rotate {
		matrix = []uint32{0, 0x10000, 0, 0xffff0000, 0, 0, 0, 0, 0x40000000}
	}
	var m []byte
	for _, x := range matrix {
		m = append(m, u32(x)...)
	}
	var tkhd []byte
	if v.tkhdVersion1 {
		tkhd = mp4box("tkhd", u32(1<<24), u64(0), u64(0), u32(1), u32(0), u64(0), make([]byte, 8), make([]byte, 8), m,
			u32(uint32(v.w)<<16), u32(uint32(v.h)<<16))
	} else {
		tkhd = mp4box("tkhd", u32(0), u32(0), u32(0), u32(1), u32(0), u32(0), make([]byte, 8), make([]byte, 8), m,
			u32(uint32(v.w)<<16), u32(uint32(v.h)<<16))
	}
	track := func(handler, codec string, frames uint32) []byte {
		mdhd := mp4box("mdhd", u32(0), u32(0), u32(0), u32(1000), u32(uint32(v.seconds*1000)), u32(0))
		hdlr := mp4box("hdlr", u32(0), u32(0), []byte(handler), make([]byte, 12))
		stsd := mp4box("stsd", u32(0), u32(1), mp4box(codec, make([]byte, 20)))
		stts := mp4box("stts", u32(0), u32(1), u32(frames), u32(1000))
		parts := [][]byte{mdhd, hdlr, mp4box("minf", mp4box("stbl", stsd, stts))}
		trak := [][]byte{mp4box("mdia", parts...)}
		if handler == "vide" {
			trak = append([][]byte{tkhd}, trak...)
		}
		return mp4box("trak", trak...)
	}
	moovParts := [][]byte{mvhd, track("vide", "avc1", v.frames)}
	if v.audio {
		moovParts = append(moovParts, track("soun", "mp4a", 0))
	}
	moov := mp4box("moov", moovParts...)
	brand := v.brand
	if brand == "" {
		brand = "isom"
	}
	ftyp := mp4box("ftyp", []byte(brand), u32(0x200), []byte("isomavc1"))
	mdat := mp4box("mdat", make([]byte, 64))
	if v.largeMdat {
		mdat = append(append(append(u32(1), "mdat"...), u64(16+64)...), make([]byte, 64)...)
	}
	if v.moovLast {
		return bytes.Join([][]byte{ftyp, mdat, moov}, nil)
	}
	return bytes.Join([][]byte{ftyp, moov, mdat}, nil)
}

func TestProbeVideo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    testVideo
		want Video
	}{
		{"fast start", testVideo{w: 1920, h: 1080, seconds: 10, frames: 300, audio: true},
			Video{Type: MP4, Width: 1920, Height: 1080, Duration: 10 * time.Second, FrameRate: 30, VideoCodec: "avc1", AudioCodec: "mp4a"}},
		{"index at the end, 64-bit mdat", testVideo{w: 1280, h: 720, seconds: 4, frames: 100, moovLast: true, largeMdat: true},
			Video{Type: MP4, Width: 1280, Height: 720, Duration: 4 * time.Second, FrameRate: 25, VideoCodec: "avc1"}},
		{"recorded upright on a phone", testVideo{brand: "qt  ", w: 1920, h: 1080, rotate: true, seconds: 3, frames: 180},
			Video{Type: QuickTime, Width: 1080, Height: 1920, Duration: 3 * time.Second, FrameRate: 60, VideoCodec: "avc1"}},
		{"64-bit track header", testVideo{w: 640, h: 480, seconds: 2, frames: 48, tkhdVersion1: true},
			Video{Type: MP4, Width: 640, Height: 480, Duration: 2 * time.Second, FrameRate: 24, VideoCodec: "avc1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := tt.v.bytes()
			got, err := ProbeVideo(bytes.NewReader(data), int64(len(data)))
			if err != nil || got != tt.want {
				t.Fatalf("ProbeVideo = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestProbeVideoRefuses(t *testing.T) {
	t.Parallel()
	whole := testVideo{w: 640, h: 480, seconds: 1, frames: 30}.bytes()
	tests := []struct {
		name string
		data []byte
	}{
		{"an image", encoded(t, PNG, 4, 4)},
		{"truncated before the index", whole[:40]},
		{"no video track", mp4box("ftyp", []byte("isom"), u32(0))},
	}
	for _, tt := range tests {
		if _, err := ProbeVideo(bytes.NewReader(tt.data), int64(len(tt.data))); err == nil {
			t.Errorf("%s: no error", tt.name)
		}
	}
	if _, err := ProbeVideo(bytes.NewReader([]byte("hello world!")), 12); !errors.Is(err, ErrNotVideo) {
		t.Errorf("text: %v", err)
	}
	if typ, ok := SniffVideo(whole); !ok || typ != MP4 || !IsVideo(typ) || IsVideo(JPEG) {
		t.Fatal("SniffVideo")
	}
}
