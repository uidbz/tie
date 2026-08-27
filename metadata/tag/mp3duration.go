// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// This file is part of the tie fork of dhowden/tag: it adds MP3 playing-time
// extraction, which upstream does not provide. It parses the first MPEG audio
// frame header after the tag, preferring a Xing/Info or VBRI frame-count header
// (accurate for VBR and for CBR files that carry one) and falling back to a
// constant-bit-rate estimate from the audio byte length.

package tag

import (
	"io"
	"time"
)

// MPEG audio bitrates in kbit/s, indexed [layer][bitrateIndex]. Layer index is
// the raw 2-bit layer field (1=Layer III, 2=Layer II, 3=Layer I); index 0 is the
// reserved layer and unused. Index 0 and 15 of the bitrate axis are "free" and
// "bad" respectively and treated as invalid by the parser.
var mp3BitrateMPEG1 = [4][16]int{
	{},
	{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}, // Layer III
	{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0}, // Layer II
	{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}, // Layer I
}

var mp3BitrateMPEG2 = [4][16]int{
	{},
	{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}, // Layer III
	{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}, // Layer II
	{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0}, // Layer I
}

// mp3SampleRates is indexed [version][sampleRateIndex]; version is the raw 2-bit
// version field (3=MPEG1, 2=MPEG2, 0=MPEG2.5; 1 is reserved).
var mp3SampleRates = map[int][3]int{
	3: {44100, 48000, 32000},
	2: {22050, 24000, 16000},
	0: {11025, 12000, 8000},
}

// mp3FrameHeader holds the fields decoded from a 4-byte MPEG audio frame header.
type mp3FrameHeader struct {
	version         int // raw 2-bit field: 3=MPEG1, 2=MPEG2, 0=MPEG2.5
	layer           int // raw 2-bit field: 1=III, 2=II, 3=I
	bitrate         int // bits per second
	sampleRate      int // Hz
	samplesPerFrame int
	channelMode     int // raw 2-bit field: 3=mono
}

// parseMP3FrameHeader decodes b[0:4] as an MPEG audio frame header, returning
// ok=false when the sync word is absent or any field is invalid/reserved.
func parseMP3FrameHeader(b []byte) (mp3FrameHeader, bool) {
	var h mp3FrameHeader
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return h, false
	}
	h.version = int(b[1]>>3) & 0x03
	h.layer = int(b[1]>>1) & 0x03
	if h.version == 1 || h.layer == 0 {
		return h, false // reserved version / reserved layer
	}
	bitrateIdx := int(b[2]>>4) & 0x0F
	srIdx := int(b[2]>>2) & 0x03
	h.channelMode = int(b[3]>>6) & 0x03
	if bitrateIdx == 0 || bitrateIdx == 15 || srIdx == 3 {
		return h, false
	}
	table := mp3BitrateMPEG2
	if h.version == 3 {
		table = mp3BitrateMPEG1
	}
	h.bitrate = table[h.layer][bitrateIdx] * 1000
	h.sampleRate = mp3SampleRates[h.version][srIdx]
	if h.bitrate == 0 || h.sampleRate == 0 {
		return h, false
	}
	switch h.layer {
	case 3: // Layer I
		h.samplesPerFrame = 384
	case 2: // Layer II
		h.samplesPerFrame = 1152
	case 1: // Layer III
		if h.version == 3 {
			h.samplesPerFrame = 1152
		} else {
			h.samplesPerFrame = 576
		}
	}
	return h, true
}

// mp3Duration computes the playing time of the MPEG audio stream beginning at
// audioStart (the byte just past any leading ID3v2 tag). It returns 0 when no
// valid frame is found. r's position is left undefined; callers seek as needed.
func mp3Duration(r io.ReadSeeker, audioStart int64) time.Duration {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil || end <= audioStart {
		return 0
	}
	// A trailing 128-byte ID3v1 tag ("TAG") is not audio; exclude it from the
	// CBR byte-length estimate.
	audioEnd := end
	if end-audioStart >= 128 {
		if _, err := r.Seek(end-128, io.SeekStart); err == nil {
			t := make([]byte, 3)
			if _, err := io.ReadFull(r, t); err == nil && string(t) == "TAG" {
				audioEnd = end - 128
			}
		}
	}

	if _, err := r.Seek(audioStart, io.SeekStart); err != nil {
		return 0
	}
	window := make([]byte, 8192)
	n, err := io.ReadAtLeast(r, window, 4)
	if err != nil {
		return 0
	}
	window = window[:n]

	// Scan for the first valid frame sync; encoders may leave a few bytes of
	// slack between the tag and the first frame.
	frameOff := -1
	var fh mp3FrameHeader
	for i := 0; i+4 <= len(window); i++ {
		if window[i] != 0xFF || window[i+1]&0xE0 != 0xE0 {
			continue
		}
		if h, ok := parseMP3FrameHeader(window[i : i+4]); ok {
			frameOff, fh = i, h
			break
		}
	}
	if frameOff < 0 {
		return 0
	}

	// A Xing/Info (Layer III) or VBRI header in the first frame carries an exact
	// frame count. Its offset past the 4-byte header equals the side-information
	// size, which depends on MPEG version and channel mode.
	sideInfo := 32
	switch {
	case fh.version == 3 && fh.channelMode == 3:
		sideInfo = 17
	case fh.version != 3 && fh.channelMode == 3:
		sideInfo = 9
	case fh.version != 3:
		sideInfo = 17
	}
	if fh.layer == 1 { // Layer III carries Xing/Info
		xo := frameOff + 4 + sideInfo
		if xo+12 <= len(window) {
			switch string(window[xo : xo+4]) {
			case "Xing", "Info":
				flags := getInt(window[xo+4 : xo+8])
				if flags&0x1 != 0 {
					if d := framesToDuration(getInt(window[xo+8:xo+12]), fh); d > 0 {
						return d
					}
				}
			}
		}
	}
	if vo := frameOff + 4 + 32; vo+18 <= len(window) && string(window[vo:vo+4]) == "VBRI" {
		if d := framesToDuration(getInt(window[vo+14:vo+18]), fh); d > 0 {
			return d
		}
	}

	// Constant-bit-rate estimate from the audio byte length.
	frameStart := audioStart + int64(frameOff)
	if audioBytes := audioEnd - frameStart; audioBytes > 0 {
		secs := float64(audioBytes*8) / float64(fh.bitrate)
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func framesToDuration(frames int, fh mp3FrameHeader) time.Duration {
	if frames <= 0 || fh.sampleRate == 0 {
		return 0
	}
	secs := float64(frames*fh.samplesPerFrame) / float64(fh.sampleRate)
	return time.Duration(secs * float64(time.Second))
}
