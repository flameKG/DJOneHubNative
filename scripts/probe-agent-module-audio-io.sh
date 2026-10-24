#!/bin/bash
# Offline PCM conversion/frame tests for the CoreAudio adapter; does not open devices.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out="${TMPDIR:-/tmp}/djonehub-module-audio-io-probe"
swiftc "$root/app/Sources/AgentModuleCoreAudioIO.swift" \
  "$root/app/Sources/AgentAudioTransport.swift" \
  "$root/app/Tests/AgentModuleAudioIOProbe/main.swift" \
  -framework CoreAudio -framework CryptoKit -o "$out"
exec "$out"
