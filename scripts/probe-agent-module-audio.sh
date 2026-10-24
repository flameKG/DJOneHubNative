#!/bin/bash
# Read-only CoreAudio/UAC device discovery; does not open streams or alter USB state.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out="${TMPDIR:-/tmp}/djonehub-module-audio-probe"
swiftc "$root/app/Sources/AgentModuleCoreAudioIO.swift" \
  "$root/app/Sources/AgentAudioTransport.swift" \
  "$root/app/Tests/AgentModuleAudioProbe/main.swift" \
  -framework CoreAudio -framework CryptoKit -o "$out"
exec "$out"
