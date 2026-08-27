#!/bin/sh
# Regenerates the audio fixtures used by duration_test.go. Each file has a
# distinct length (2/3/4/5/6s) so a misparsed duration cannot pass by matching
# another fixture or a hardcoded constant. Requires ffmpeg with libmp3lame,
# flac, libopus, libvorbis and aac encoders.
set -e
cd "$(dirname "$0")"

gen() { # duration outfile sample_rate codec_args...
	dur="$1"; out="$2"; sr="$3"; shift 3
	ffmpeg -hide_banner -loglevel error -y \
		-f lavfi -i "sine=frequency=440:duration=$dur:sample_rate=$sr" \
		-ac 1 "$@" "$out"
}

gen 2 dur.mp3  44100 -c:a libmp3lame -b:a 96k
gen 3 dur.flac 44100 -c:a flac -compression_level 0
gen 4 dur.opus 48000 -c:a libopus -b:a 32k
gen 5 dur.ogg  44100 -c:a libvorbis -q:a 1
gen 6 dur.m4a  44100 -c:a aac -b:a 64k
