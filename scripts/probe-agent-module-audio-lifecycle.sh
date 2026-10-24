#!/bin/sh
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
out="${TMPDIR:-/tmp}/djonehub-module-audio-lifecycle-probe"
swiftc -parse-as-library "$root/app/Sources/AgentModuleCoreAudioIO.swift" \
  "$root/app/Sources/AgentAudioTransport.swift" \
  "$root/app/Tests/AgentModuleAudioLifecycleProbe/main.swift" \
  -framework CoreAudio -framework CryptoKit -o "$out"
exec "$out"
