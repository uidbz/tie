// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"errors"
	"io"
	"time"
)

// blockType is a type which represents an enumeration of valid FLAC blocks
type blockType byte

// FLAC block types.
const (
	streamInfoBlock blockType = 0
	// Padding Block               1
	// Application Block           2
	// Seektable Block             3
	// Cue Sheet Block             5
	vorbisCommentBlock blockType = 4
	pictureBlock       blockType = 6
)

// ReadFLACTags reads FLAC metadata from the io.ReadSeeker, returning the resulting
// metadata in a Metadata implementation, or non-nil error if there was a problem.
func ReadFLACTags(r io.ReadSeeker) (Metadata, error) {
	flac, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if flac != "fLaC" {
		return nil, errors.New("expected 'fLaC'")
	}

	m := &metadataFLAC{
		newMetadataVorbis(),
	}

	for {
		last, err := m.readFLACMetadataBlock(r)
		if err != nil {
			return nil, err
		}

		if last {
			break
		}
	}
	return m, nil
}

type metadataFLAC struct {
	*metadataVorbis
}

func (m *metadataFLAC) readFLACMetadataBlock(r io.ReadSeeker) (last bool, err error) {
	blockHeader, err := readBytes(r, 1)
	if err != nil {
		return
	}

	if getBit(blockHeader[0], 7) {
		blockHeader[0] ^= (1 << 7)
		last = true
	}

	blockLen, err := readInt(r, 3)
	if err != nil {
		return
	}

	switch blockType(blockHeader[0]) {
	case streamInfoBlock:
		err = m.readStreamInfo(r, blockLen)

	case vorbisCommentBlock:
		err = m.readVorbisComment(r)

	case pictureBlock:
		err = m.readPictureBlock(r)

	default:
		_, err = r.Seek(int64(blockLen), io.SeekCurrent)
	}
	return
}

// readStreamInfo parses the mandatory STREAMINFO block (the first metadata
// block) for the sample rate and total sample count, from which the playing
// time follows. The relevant fields are packed as: sample rate (20 bits),
// channels (3), bits-per-sample (5), total samples (36), starting 10 bytes into
// the block (after the min/max block and frame sizes).
func (m *metadataFLAC) readStreamInfo(r io.ReadSeeker, blockLen int) error {
	b, err := readBytes(r, uint(blockLen))
	if err != nil {
		return err
	}
	if len(b) < 18 {
		return nil // malformed; leave duration unset rather than fail the read
	}
	// Sample rate is 20 bits (b[10], b[11], high nibble of b[12]); then 3 bits of
	// channels and 5 bits of bits-per-sample fill out b[12] and the high nibble of
	// b[13]; the 36-bit total sample count is the low nibble of b[13] followed by
	// b[14..17].
	sampleRate := int(b[10])<<12 | int(b[11])<<4 | int(b[12])>>4
	totalSamples := int64(b[13]&0x0F)<<32 | int64(b[14])<<24 | int64(b[15])<<16 | int64(b[16])<<8 | int64(b[17])
	if sampleRate > 0 && totalSamples > 0 {
		secs := float64(totalSamples) / float64(sampleRate)
		m.duration = time.Duration(secs * float64(time.Second))
	}
	return nil
}

func (m *metadataFLAC) FileType() FileType {
	return FLAC
}
