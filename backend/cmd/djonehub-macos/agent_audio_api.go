package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
)

type agentAudioFormat struct {
	Encoding          string `json:"encoding"`
	SampleRate        int    `json:"sample_rate"`
	Channels          int    `json:"channels"`
	FrameMilliseconds int    `json:"frame_ms"`
	FrameBytes        int    `json:"frame_bytes"`
}

type agentAudioState struct {
	Compiled              bool             `json:"compiled"`
	TransportReady        bool             `json:"transport_ready"`
	ModuleIOReady         bool             `json:"module_io_ready"`
	PCMTransportAvailable bool             `json:"pcm_transport_available"`
	VoiceAgentAvailable   bool             `json:"voice_agent_available"`
	Active                bool             `json:"active"`
	SocketPath            string           `json:"socket_path,omitempty"`
	ClientConnected       bool             `json:"client_connected"`
	CaptureActive         bool             `json:"capture_active"`
	CaptureSessionID      string           `json:"capture_session_id,omitempty"`
	FramesReceived        uint64           `json:"frames_received"`
	NonzeroFrames         uint64           `json:"nonzero_frames"`
	BytesReceived         uint64           `json:"bytes_received"`
	PeakSample            int              `json:"peak_sample"`
	CaptureError          string           `json:"capture_error,omitempty"`
	ASRAvailable          bool             `json:"asr_available"`
	TTSAvailable          bool             `json:"tts_available"`
	Format                agentAudioFormat `json:"format"`
	Error                 string           `json:"error,omitempty"`
}

type agentAudioTransportUpdate struct {
	Compiled        bool   `json:"compiled"`
	TransportReady  bool   `json:"transport_ready"`
	ModuleIOReady   bool   `json:"module_io_ready"`
	SocketPath      string `json:"socket_path"`
	ClientConnected bool   `json:"client_connected"`
	Error           string `json:"error"`
}

func fixedAgentAudioFormat() agentAudioFormat {
	return agentAudioFormat{
		Encoding: "pcm_s16le", SampleRate: 8000, Channels: 1,
		FrameMilliseconds: 20, FrameBytes: 320,
	}
}

func (a *app) agentAudioSnapshot() agentAudioState {
	a.agentAudioMu.RLock()
	state := a.agentAudio
	a.agentAudioMu.RUnlock()
	state.Format = fixedAgentAudioFormat()
	state.PCMTransportAvailable = state.Compiled && state.TransportReady && strings.TrimSpace(state.SocketPath) != ""
	state.VoiceAgentAvailable = state.PCMTransportAvailable && state.ModuleIOReady
	state.ASRAvailable = false
	state.TTSAvailable = false
	if !state.VoiceAgentAvailable {
		state.Active = false
	}
	return state
}

func (a *app) agentAudioTransport(w http.ResponseWriter, r *http.Request) {
	if principalFromRequest(r).Kind == "cli" {
		writeCodedError(w, http.StatusForbidden, "cli_route_unavailable", "该路由仅供原生应用注册传输状态", false)
		return
	}
	defer r.Body.Close()
	var update agentAudioTransportUpdate
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&update); err != nil {
		writeCodedError(w, http.StatusBadRequest, "invalid_agent_audio_state", "Agent 音频传输状态无效", false)
		return
	}
	update.SocketPath = strings.TrimSpace(update.SocketPath)
	if update.TransportReady && (!update.Compiled || update.SocketPath == "" || !filepath.IsAbs(update.SocketPath)) {
		writeCodedError(w, http.StatusBadRequest, "invalid_agent_audio_transport", "就绪的 Agent 音频传输必须由已编译功能提供绝对 socket 路径", false)
		return
	}
	a.agentAudioMu.Lock()
	previousSocket := a.agentAudio.SocketPath
	previousTransportReady := a.agentAudio.TransportReady
	a.agentAudio.Compiled = update.Compiled
	a.agentAudio.TransportReady = update.TransportReady
	a.agentAudio.ModuleIOReady = update.ModuleIOReady
	a.agentAudio.SocketPath = update.SocketPath
	a.agentAudio.ClientConnected = update.ClientConnected
	a.agentAudio.Active = a.agentAudio.Active && update.Compiled && update.TransportReady && update.ModuleIOReady
	a.agentAudio.Error = strings.TrimSpace(update.Error)
	a.agentAudioMu.Unlock()
	if previousTransportReady && (!update.Compiled || !update.TransportReady || previousSocket != update.SocketPath) {
		a.stopVoiceVerificationAudio()
	}
	writeJSON(w, http.StatusOK, a.agentAudioSnapshot())
}

func (a *app) agentAudioStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.agentAudioSnapshot())
}

func (a *app) agentAudioStart(w http.ResponseWriter, _ *http.Request) {
	state := a.agentAudioSnapshot()
	if !state.VoiceAgentAvailable {
		writeCodedError(w, http.StatusConflict, "agent_audio_transport_unavailable", "Agent PCM 传输尚未就绪", true)
		return
	}
	a.agentAudioMu.Lock()
	a.agentAudio.Active = true
	a.agentAudioMu.Unlock()
	writeJSON(w, http.StatusOK, a.agentAudioSnapshot())
}

func (a *app) agentAudioStop(w http.ResponseWriter, _ *http.Request) {
	a.stopVoiceVerificationAudio()
	a.updateVoiceVerification(func(window *voiceVerificationWindow) {
		window.AudioAuthorized = false
	})
	a.agentAudioMu.Lock()
	a.agentAudio.Active = false
	a.agentAudioMu.Unlock()
	writeJSON(w, http.StatusOK, a.agentAudioSnapshot())
}
