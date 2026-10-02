#!/bin/sh
# Generates the test song library in e2e/media (needs Docker).
#   - five songs used by name in the tests, two with very long titles
#   - an original-vocal companion for 倔強 (different picture and pitch)
#   - 70 short filler songs so search results span several pages
set -e
cd "$(dirname "$0")"
mkdir -p media
docker run --rm -v "$(pwd)/media:/m" -w /m alpine:3.22 sh -c '
  set -e
  apk add -q ffmpeg >/dev/null
  enc="-c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac -loglevel error -y"
  ffmpeg -f lavfi -i testsrc=size=640x360:rate=25:duration=60 -f lavfi -i sine=frequency=440:duration=60 $enc base.mp4
  ffmpeg -f lavfi -i smptebars=size=640x360:rate=25:duration=60 -f lavfi -i sine=frequency=880:duration=60 $enc orig.mp4
  ffmpeg -f lavfi -i testsrc=size=160x90:rate=10:duration=2 -f lavfi -i sine=duration=2 $enc short.mp4
  for n in "五月天 - 倔強" "周杰倫 - 晴天" "孫燕姿 - 遇見" \
    "Queen - Bohemian Rhapsody (2011 Remaster) [Official Music Video with Lyrics and Extended Ending]" \
    "一個名字非常非常長的歌手樂團 - 這是一首名字長到一定會超出手機螢幕寬度而且還會繼續延伸下去的歌曲標題"; do
    cp base.mp4 "$n.mp4"
  done
  mv orig.mp4 "五月天 - 倔強_original.mp4"
  for i in $(seq -w 1 70); do cp short.mp4 "Filler - Song $i.mp4"; done
  rm base.mp4 short.mp4
'
echo "media ready: $(ls media | wc -l) files"
