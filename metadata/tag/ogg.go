// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	vorbisCommentPrefix = []byte("\x03vorbis")
	opusTagsPrefix      = []byte("OpusTags")
	vorbisInfoPrefix    = []byte("\x01vorbis")
	opusHeadPrefix      = []byte("OpusHead")
)

var oggCRC32Poly04c11db7 = oggCRCTable(0x04c11db7)

type crc32Table [256]uint32

func oggCRCTable(poly uint32) *crc32Table {
	var t crc32Table

	for i := 0; i < 256; i++ {
		crc := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ poly
			} else {
				crc <<= 1
			}
		}
		t[i] = crc
	}

	return &t
}

func oggCRCUpdate(crc uint32, tab *crc32Table, p []byte) uint32 {
	for _, v := range p {
		crc = (crc << 8) ^ tab[byte(crc>>24)^v]
	}
	return crc
}

type oggPageHeader struct {
	Magic           [4]byte // "OggS"
	Version         uint8
	Flags           uint8
	GranulePosition uint64
	SerialNumber    uint32
	SequenceNumber  uint32
	CRC             uint32
	Segments        uint8
}

type oggDemuxer struct {
	packetBufs map[uint32]*bytes.Buffer
}

// Read ogg packets, can return empty slice of packets and nil err
// if more data is needed
func (o *oggDemuxer) Read(r io.Reader) ([][]byte, error) {
	headerBuf := &bytes.Buffer{}
	var oh oggPageHeader
	if err := binary.Read(io.TeeReader(r, headerBuf), binary.LittleEndian, &oh); err != nil {
		return nil, err
	}

	if bytes.Compare(oh.Magic[:], []byte("OggS")) != 0 {
		// TODO: seek for syncword?
		return nil, errors.New("expected 'OggS'")
	}

	segmentTable := make([]byte, oh.Segments)
	if _, err := io.ReadFull(r, segmentTable); err != nil {
		return nil, err
	}
	var segmentsSize int64
	for _, s := range segmentTable {
		segmentsSize += int64(s)
	}
	segmentsData := make([]byte, segmentsSize)
	if _, err := io.ReadFull(r, segmentsData); err != nil {
		return nil, err
	}

	headerBytes := headerBuf.Bytes()
	// reset CRC to zero in header before checksum
	headerBytes[22] = 0
	headerBytes[23] = 0
	headerBytes[24] = 0
	headerBytes[25] = 0
	crc := oggCRCUpdate(0, oggCRC32Poly04c11db7, headerBytes)
	crc = oggCRCUpdate(crc, oggCRC32Poly04c11db7, segmentTable)
	crc = oggCRCUpdate(crc, oggCRC32Poly04c11db7, segmentsData)
	if crc != oh.CRC {
		return nil, fmt.Errorf("expected crc %x != %x", oh.CRC, crc)
	}

	if o.packetBufs == nil {
		o.packetBufs = map[uint32]*bytes.Buffer{}
	}

	var packetBuf *bytes.Buffer
	continued := oh.Flags&0x1 != 0
	if continued {
		if b, ok := o.packetBufs[oh.SerialNumber]; ok {
			packetBuf = b
		} else {
			return nil, fmt.Errorf("could not find continued packet %d", oh.SerialNumber)
		}
	} else {
		packetBuf = &bytes.Buffer{}
	}

	var packets [][]byte
	var p int
	for _, s := range segmentTable {
		packetBuf.Write(segmentsData[p : p+int(s)])
		if s < 255 {
			packets = append(packets, packetBuf.Bytes())
			packetBuf = &bytes.Buffer{}
		}
		p += int(s)
	}

	o.packetBufs[oh.SerialNumber] = packetBuf

	return packets, nil
}

// ReadOGGTags reads OGG metadata from the io.ReadSeeker, returning the resulting
// metadata in a Metadata implementation, or non-nil error if there was a problem.
// See http://www.xiph.org/vorbis/doc/Vorbis_I_spec.html
// and http://www.xiph.org/ogg/doc/framing.html for details.
// For Opus see https://tools.ietf.org/html/rfc7845
func ReadOGGTags(r io.ReadSeeker) (Metadata, error) {
	od := &oggDemuxer{}
	// The identification header (first packet) carries the sample-rate clock we
	// need to turn the final granule position into a playing time: Opus is always
	// clocked at 48 kHz and its OpusHead gives the pre-skip to subtract; Vorbis
	// carries its own sample rate.
	var isOpus bool
	var sampleRate uint32
	var preSkip uint16
	var m *metadataOGG
	for m == nil {
		bs, err := od.Read(r)
		if err != nil {
			return nil, err
		}

		for _, b := range bs {
			switch {
			case bytes.HasPrefix(b, opusHeadPrefix):
				isOpus = true
				if len(b) >= 12 {
					preSkip = binary.LittleEndian.Uint16(b[10:12])
				}
			case bytes.HasPrefix(b, vorbisInfoPrefix):
				if len(b) >= 16 {
					sampleRate = binary.LittleEndian.Uint32(b[12:16])
				}
			case bytes.HasPrefix(b, vorbisCommentPrefix):
				mm := &metadataOGG{newMetadataVorbis()}
				if err := mm.readVorbisComment(bytes.NewReader(b[len(vorbisCommentPrefix):])); err != nil {
					return nil, err
				}
				m = mm
			case bytes.HasPrefix(b, opusTagsPrefix):
				mm := &metadataOGG{newMetadataVorbis()}
				if err := mm.readVorbisComment(bytes.NewReader(b[len(opusTagsPrefix):])); err != nil {
					return nil, err
				}
				m = mm
			}
		}
	}

	// The last Ogg page's granule position is the total number of decoded
	// samples on the logical stream; dividing by the sample-rate clock (less the
	// Opus pre-skip) gives the playing time.
	if g, ok := lastOggGranule(r); ok {
		switch {
		case isOpus && g > uint64(preSkip):
			m.duration = time.Duration(g-uint64(preSkip)) * time.Second / 48000
		case !isOpus && sampleRate > 0:
			m.duration = time.Duration(g) * time.Second / time.Duration(sampleRate)
		}
	}
	return m, nil
}

// lastOggGranule scans the tail of an Ogg stream for the final page header and
// returns its granule position. It reads a bounded window from the end (an Ogg
// page is at most ~64 KiB) and locates the last "OggS" capture pattern; the
// granule position is the little-endian uint64 six bytes past it.
func lastOggGranule(r io.ReadSeeker) (uint64, bool) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, false
	}
	const window = 65536
	start := end - window
	if start < 0 {
		start = 0
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return 0, false
	}
	buf := make([]byte, end-start)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, false
	}
	idx := bytes.LastIndex(buf, []byte("OggS"))
	if idx < 0 || idx+14 > len(buf) {
		return 0, false
	}
	return binary.LittleEndian.Uint64(buf[idx+6 : idx+14]), true
}

type metadataOGG struct {
	*metadataVorbis
}

func (m *metadataOGG) FileType() FileType {
	return OGG
}
