package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVoiceVerificationDisarmAndAgentAudioStopDoNotRestartCapture(t *testing.T) {
	for _, test := range []struct {
		name string
		stop func(*app)
	}{
		{name: "disarm", stop: func(instance *app) { instance.disarmVoiceVerification() }},
		{name: "agent audio stop", stop: func(instance *app) {
			instance.agentAudioStop(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/agent/audio/stop", nil))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			starts := make(chan struct{}, 2)
			instance := &app{}
			instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
			instance.agentPCMRunner = func(ctx context.Context, _ string, _ func([]byte)) error {
				starts <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			}
			instance.voiceVerification = &voiceVerificationWindow{Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-1"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			call := callStateInfo{State: "active", Active: true, SessionID: "session-1"}
			instance.reconcileVoiceVerificationAudio(ctx, call)
			<-starts
			test.stop(instance)
			instance.reconcileVoiceVerificationAudio(ctx, call)
			select {
			case <-starts:
				t.Fatal("capture restarted after explicit stop")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestVoiceVerificationDisarmAndAgentAudioStopCancelCapture(t *testing.T) {
	for _, test := range []struct {
		name string
		stop func(*app)
	}{
		{name: "disarm", stop: func(instance *app) { instance.disarmVoiceVerification() }},
		{name: "rearm", stop: func(instance *app) { instance.armVoiceVerification(time.Minute, []string{"18742589885"}) }},
		{name: "agent audio stop", stop: func(instance *app) {
			instance.agentAudioStop(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/agent/audio/stop", nil))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			stopped := make(chan struct{}, 1)
			instance := &app{}
			instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
			instance.agentPCMRunner = func(ctx context.Context, _ string, _ func([]byte)) error {
				started <- struct{}{}
				<-ctx.Done()
				stopped <- struct{}{}
				return ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			instance.startVoiceVerificationAudio(ctx, "session-1")
			<-started
			test.stop(instance)
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("capture was not cancelled")
			}
		})
	}
}

func TestVoiceVerificationStopsPCMWhenAuthorizedSessionChanges(t *testing.T) {
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	instance := &app{}
	instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
	instance.agentPCMRunner = func(ctx context.Context, _ string, _ func([]byte)) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return ctx.Err()
	}
	instance.voiceVerification = &voiceVerificationWindow{Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-1"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "active", Active: true, SessionID: "session-1"})
	<-started
	instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "active", Active: true, SessionID: "session-2"})
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("PCM client remained active after call session changed")
	}
}

func TestVoiceVerificationRejectsLateFramesAndClearsSessionMetrics(t *testing.T) {
	callbacks := make(chan func([]byte), 2)
	instance := &app{}
	instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
	instance.agentPCMRunner = func(ctx context.Context, _ string, onPCM func([]byte)) error {
		callbacks <- onPCM
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance.startVoiceVerificationAudio(ctx, "session-1")
	oldCallback := <-callbacks
	instance.startVoiceVerificationAudio(ctx, "session-2")
	newCallback := <-callbacks
	oldFrame := make([]byte, 320)
	oldFrame[0] = 1
	oldCallback(oldFrame)
	if state := instance.agentAudioSnapshot(); state.FramesReceived != 0 {
		t.Fatalf("late frame from old session was counted: %+v", state)
	}
	newFrame := make([]byte, 320)
	newFrame[0] = 2
	newCallback(newFrame)
	if state := instance.agentAudioSnapshot(); state.FramesReceived != 1 || state.CaptureSessionID != "session-2" {
		t.Fatalf("new session frame was not counted: %+v", state)
	}
	instance.stopVoiceVerificationAudio()
	state := instance.agentAudioSnapshot()
	if state.CaptureSessionID != "" || state.FramesReceived != 0 || state.NonzeroFrames != 0 || state.BytesReceived != 0 || state.PeakSample != 0 || state.CaptureError != "" || state.LastCapture == nil || state.LastCapture.SessionID != "session-2" || state.LastCapture.FramesReceived != 1 || state.LastCapture.NonzeroFrames != 1 {
		t.Fatalf("capture identity or metrics survived stop incorrectly: %+v", state)
	}
}

func TestVoiceVerificationStartsPCMForAnsweredActiveCallAndStopsOnIdle(t *testing.T) {
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	instance := &app{}
	instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
	instance.agentPCMRunner = func(ctx context.Context, _ string, onPCM func([]byte)) error {
		started <- struct{}{}
		pcm := make([]byte, 320)
		pcm[0], pcm[1] = 1, 0
		onPCM(pcm)
		<-ctx.Done()
		stopped <- struct{}{}
		return ctx.Err()
	}
	instance.voiceVerification = &voiceVerificationWindow{Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-1"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "active", Active: true, SessionID: "session-1"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("PCM client was not started")
	}
	deadline := time.Now().Add(time.Second)
	for {
		state := instance.agentAudioSnapshot()
		if state.CaptureActive && state.FramesReceived == 1 && state.NonzeroFrames == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PCM metrics not updated: %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}

	instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "idle"})
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("PCM client was not stopped")
	}
	if state := instance.agentAudioSnapshot(); state.CaptureActive {
		t.Fatalf("capture remained active: %+v", state)
	}
}

func TestVoiceVerificationWindowIsSingleUseAndScoped(t *testing.T) {
	instance := &app{}
	window := instance.armVoiceVerification(5*time.Minute, []string{"18742589885"})
	if !window.Active || len(window.AllowedNumbers) != 1 {
		t.Fatalf("unexpected window: %+v", window)
	}
	if !instance.voiceVerificationNumberAllowed("18742589885") {
		t.Fatal("allowed number was rejected")
	}
	if instance.voiceVerificationNumberAllowed("13800138000") {
		t.Fatal("unlisted number was allowed")
	}
	instance.updateVoiceVerification(func(current *voiceVerificationWindow) {
		current.Active = false
		current.Answered = true
		current.Consumed = true
	})
	if got := instance.voiceVerificationSnapshot(); got.Active || !got.Answered || !got.Consumed {
		t.Fatalf("window was not consumed: %+v", got)
	}
}

func TestVoiceVerificationWindowExpires(t *testing.T) {
	instance := &app{}
	instance.armVoiceVerification(30*time.Second, []string{"18742589885"})
	instance.updateVoiceVerification(func(current *voiceVerificationWindow) {
		current.ExpiresAt = time.Now().Add(-time.Second)
	})
	if got := instance.voiceVerificationSnapshot(); got.Active {
		t.Fatalf("expired window remained active: %+v", got)
	}
}
