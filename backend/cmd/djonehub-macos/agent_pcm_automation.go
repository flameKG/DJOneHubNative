package main

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

type agentPCMRunnerFunc func(context.Context, string, func([]byte)) error

func (a *app) reconcileVoiceVerificationAudio(ctx context.Context, call callStateInfo) {
	window := a.voiceVerificationSnapshot()
	authorized := call.State == "active" && call.Active && window.Answered && window.Consumed && window.AudioAuthorized &&
		window.SessionID != "" && call.SessionID == window.SessionID
	if authorized {
		if err := a.ensureModuleVoiceRoute(call.SessionID); err != nil {
			a.stopVoiceVerificationAudio()
			a.agentAudioMu.Lock()
			a.agentAudio.CaptureError = "模块语音路由启动失败：" + err.Error()
			a.agentAudioMu.Unlock()
			return
		}
		a.startVoiceVerificationAudio(ctx, call.SessionID)
		return
	}
	// Fail closed: any transition away from the exact authorized active session
	// immediately revokes capture, including held/unknown/replaced calls.
	a.stopVoiceVerificationAudio()
	a.stopModuleVoiceRouteForCall()
}

func (a *app) ensureModuleVoiceRoute(sessionID string) error {
	if !a.moduleVoiceAutoStart {
		return nil
	}
	a.moduleVoiceMu.Lock()
	if a.moduleVoiceRoute {
		a.moduleVoiceMu.Unlock()
		return nil
	}
	if a.moduleVoiceRouteAttemptedSession == sessionID {
		err := a.moduleVoiceRouteLastError
		a.moduleVoiceMu.Unlock()
		if err == "" {
			return errors.New("模块语音路由启动未完成")
		}
		return errors.New(err)
	}
	a.moduleVoiceRouteAttemptedSession = sessionID
	a.moduleVoiceRouteLastError = ""
	starter := a.moduleVoiceRouteStart
	a.moduleVoiceMu.Unlock()
	if starter == nil {
		starter = func(directory string) error {
			return a.startModuleVoiceRoute(directory)
		}
	}
	if err := starter(a.runtimeDirectory()); err != nil {
		a.moduleVoiceMu.Lock()
		a.moduleVoiceRouteLastError = err.Error()
		a.moduleVoiceMu.Unlock()
		return err
	}
	a.moduleVoiceMu.Lock()
	a.moduleVoiceRoute = true
	a.moduleVoiceRouteLastError = ""
	a.moduleVoiceMu.Unlock()
	return nil
}

func (a *app) stopModuleVoiceRouteForCall() {
	if !a.moduleVoiceAutoStart {
		return
	}
	a.moduleVoiceMu.Lock()
	active := a.moduleVoiceRoute
	stopper := a.moduleVoiceRouteStop
	a.moduleVoiceRouteAttemptedSession = ""
	a.moduleVoiceRouteLastError = ""
	a.moduleVoiceMu.Unlock()
	if !active {
		return
	}
	if stopper == nil {
		stopper = func(directory string) error {
			return a.stopModuleVoiceRoute(directory)
		}
	}
	if err := stopper(a.runtimeDirectory()); err != nil {
		a.agentAudioMu.Lock()
		a.agentAudio.CaptureError = "模块语音路由清理失败：" + err.Error()
		a.agentAudioMu.Unlock()
		return
	}
	a.moduleVoiceMu.Lock()
	a.moduleVoiceRoute = false
	a.moduleVoiceMu.Unlock()
}

func (a *app) startVoiceVerificationAudio(parent context.Context, sessionID string) {
	transport := a.agentAudioSnapshot()
	if !transport.PCMTransportAvailable || strings.TrimSpace(transport.SocketPath) == "" {
		a.stopVoiceVerificationAudio()
		a.agentAudioMu.Lock()
		a.agentAudio.CaptureError = "agent_pcm_transport_unavailable"
		a.agentAudioMu.Unlock()
		return
	}

	a.agentPCMMu.Lock()
	if a.agentPCMCancel != nil && a.agentPCMSessionID == sessionID {
		a.agentPCMMu.Unlock()
		return
	}
	if a.agentPCMCancel != nil {
		a.agentPCMCancel()
	}
	a.agentPCMGeneration++
	generation := a.agentPCMGeneration
	ctx, cancel := context.WithCancel(parent)
	a.agentPCMCancel = cancel
	a.agentPCMSessionID = sessionID
	runner := a.agentPCMRunner
	if runner == nil {
		runner = runAgentPCMClient
	}
	// Commit the runtime state while holding the same lifecycle lock that
	// stop/completion use, so an old start cannot resurrect capture after stop.
	a.agentAudioMu.Lock()
	a.agentAudio.CaptureActive = true
	a.agentAudio.CaptureSessionID = sessionID
	a.agentAudio.CaptureStartedAt = time.Now()
	a.agentAudio.FramesReceived = 0
	a.agentAudio.NonzeroFrames = 0
	a.agentAudio.BytesReceived = 0
	a.agentAudio.PeakSample = 0
	a.agentAudio.CaptureError = ""
	a.agentAudioMu.Unlock()
	a.agentPCMMu.Unlock()

	go func() {
		err := runner(ctx, transport.SocketPath, func(frame []byte) {
			a.recordAgentPCMFrame(generation, frame)
		})
		a.agentPCMMu.Lock()
		if a.agentPCMGeneration != generation {
			a.agentPCMMu.Unlock()
			return
		}
		a.agentPCMCancel = nil
		a.agentPCMSessionID = ""
		captureError := ""
		if err != nil && !errors.Is(err, context.Canceled) {
			captureError = err.Error()
		}
		a.agentAudioMu.Lock()
		if a.agentAudio.CaptureSessionID != "" {
			a.agentAudio.LastCapture = &captureMetrics{
				SessionID: a.agentAudio.CaptureSessionID, FramesReceived: a.agentAudio.FramesReceived,
				NonzeroFrames: a.agentAudio.NonzeroFrames, BytesReceived: a.agentAudio.BytesReceived,
				PeakSample: a.agentAudio.PeakSample, StartedAt: a.agentAudio.CaptureStartedAt,
				EndedAt: time.Now(), Error: captureError,
			}
		}
		a.agentAudio.CaptureActive = false
		a.agentAudio.CaptureSessionID = ""
		a.agentAudio.CaptureStartedAt = time.Time{}
		a.agentAudio.FramesReceived = 0
		a.agentAudio.NonzeroFrames = 0
		a.agentAudio.BytesReceived = 0
		a.agentAudio.PeakSample = 0
		a.agentAudio.CaptureError = captureError
		a.agentAudioMu.Unlock()
		a.agentPCMMu.Unlock()
	}()
}

func (a *app) stopVoiceVerificationAudio() {
	a.agentPCMMu.Lock()
	a.agentPCMGeneration++
	cancel := a.agentPCMCancel
	a.agentPCMCancel = nil
	a.agentPCMSessionID = ""
	a.agentAudioMu.Lock()
	if a.agentAudio.CaptureSessionID != "" {
		a.agentAudio.LastCapture = &captureMetrics{
			SessionID: a.agentAudio.CaptureSessionID, FramesReceived: a.agentAudio.FramesReceived,
			NonzeroFrames: a.agentAudio.NonzeroFrames, BytesReceived: a.agentAudio.BytesReceived,
			PeakSample: a.agentAudio.PeakSample, StartedAt: a.agentAudio.CaptureStartedAt,
			EndedAt: time.Now(),
		}
	}
	a.agentAudio.CaptureActive = false
	a.agentAudio.CaptureSessionID = ""
	a.agentAudio.CaptureStartedAt = time.Time{}
	a.agentAudio.FramesReceived = 0
	a.agentAudio.NonzeroFrames = 0
	a.agentAudio.BytesReceived = 0
	a.agentAudio.PeakSample = 0
	a.agentAudio.CaptureError = ""
	a.agentAudioMu.Unlock()
	a.agentPCMMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *app) recordAgentPCMFrame(generation uint64, frame []byte) {
	if len(frame) != agentPCMFrameBytes {
		return
	}
	a.agentPCMMu.Lock()
	defer a.agentPCMMu.Unlock()
	if a.agentPCMGeneration != generation || a.agentPCMCancel == nil {
		return
	}
	nonzero := false
	peak := 0
	for offset := 0; offset+1 < len(frame); offset += 2 {
		sample := int(int16(binary.LittleEndian.Uint16(frame[offset : offset+2])))
		if sample != 0 {
			nonzero = true
		}
		absolute := sample
		if absolute < 0 {
			absolute = -absolute
		}
		if absolute > peak {
			peak = absolute
		}
	}
	a.agentAudioMu.Lock()
	a.agentAudio.FramesReceived++
	a.agentAudio.BytesReceived += uint64(len(frame))
	if nonzero {
		a.agentAudio.NonzeroFrames++
	}
	if peak > a.agentAudio.PeakSample {
		a.agentAudio.PeakSample = peak
	}
	a.agentAudioMu.Unlock()
}
