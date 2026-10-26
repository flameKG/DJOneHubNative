package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestVoiceVerificationStartsModuleRouteBeforeAgentPCM(t *testing.T) {
	moduleStarted := make(chan struct{}, 1)
	agentStarted := make(chan struct{}, 1)
	instance := &app{
		moduleVoiceAutoStart: true,
		moduleVoiceRouteStart: func(string) error {
			moduleStarted <- struct{}{}
			return nil
		},
	}
	instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
	instance.agentPCMRunner = func(ctx context.Context, _ string, _ func([]byte)) error {
		agentStarted <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	instance.voiceVerification = &voiceVerificationWindow{Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-1"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "active", Active: true, SessionID: "session-1"})
	select {
	case <-moduleStarted:
	case <-time.After(time.Second):
		t.Fatal("module voice route was not started")
	}
	select {
	case <-agentStarted:
	case <-time.After(time.Second):
		t.Fatal("Agent PCM was not started after module route")
	}
}

func TestVoiceVerificationRecoversModuleRouteOnceAfterModuleIOFailure(t *testing.T) {
	events := make(chan string, 8)
	var attempts atomic.Int32
	instance := &app{
		moduleVoiceAutoStart: true,
		moduleVoiceRouteStart: func(string) error {
			events <- "module-start"
			return nil
		},
		moduleVoiceRouteStop: func(string) error {
			events <- "module-stop"
			return nil
		},
	}
	instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
	instance.agentPCMRunner = func(ctx context.Context, _ string, _ func([]byte)) error {
		attempt := attempts.Add(1)
		if attempt == 1 {
			events <- "agent-failed"
			return errors.New("Agent PCM server: module_io_unavailable")
		}
		events <- "agent-retried"
		return errors.New("Agent PCM server: module_io_unavailable")
	}
	instance.voiceVerification = &voiceVerificationWindow{Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-1"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := callStateInfo{State: "active", Active: true, SessionID: "session-1"}

	instance.reconcileVoiceVerificationAudio(ctx, call)
	for _, expected := range []string{"module-start", "agent-failed", "module-stop"} {
		select {
		case actual := <-events:
			if actual != expected {
				t.Fatalf("event=%q want=%q", actual, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", expected)
		}
	}

	instance.reconcileVoiceVerificationAudio(ctx, call)
	for _, expected := range []string{"module-start", "agent-retried"} {
		select {
		case actual := <-events:
			if actual != expected {
				t.Fatalf("event=%q want=%q", actual, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", expected)
		}
	}
	select {
	case unexpected := <-events:
		t.Fatalf("automatic recovery repeated after retry: %s", unexpected)
	case <-time.After(100 * time.Millisecond):
	}
	deadline := time.Now().Add(time.Second)
	for {
		state := instance.agentAudioSnapshot()
		if state.CaptureError == "模块音频单次自动恢复已用尽，需要授权重启 CoreAudio" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing exhausted recovery error: %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	instance.reconcileVoiceVerificationAudio(ctx, call)
	select {
	case unexpected := <-events:
		t.Fatalf("Agent PCM restarted after recovery was exhausted: %s", unexpected)
	case <-time.After(100 * time.Millisecond):
	}
	if attempts.Load() != 2 {
		t.Fatalf("Agent PCM attempts=%d want=2", attempts.Load())
	}
}

func TestStaleModuleVoiceRecoveryCannotStopReplacementSession(t *testing.T) {
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	newSessionStarted := make(chan struct{}, 1)
	var routeStarts atomic.Int32
	var routeStops atomic.Int32
	instance := &app{
		moduleVoiceAutoStart: true,
		moduleVoiceRouteStart: func(string) error {
			if routeStarts.Add(1) == 2 {
				newSessionStarted <- struct{}{}
			}
			return nil
		},
		moduleVoiceRouteStop: func(string) error {
			if routeStops.Add(1) == 1 {
				close(stopEntered)
				<-releaseStop
			}
			return nil
		},
	}
	instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
	instance.agentPCMRunner = func(context.Context, string, func([]byte)) error {
		return errors.New("Agent PCM server: module_io_unavailable")
	}
	instance.voiceVerification = &voiceVerificationWindow{Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-1"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "active", Active: true, SessionID: "session-1"})
	select {
	case <-stopEntered:
	case <-time.After(time.Second):
		t.Fatal("old recovery did not enter stopper")
	}

	instance.stopVoiceVerificationAudio()
	instance.voiceVerificationMu.Lock()
	instance.voiceVerification = &voiceVerificationWindow{Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-2"}
	instance.voiceVerificationMu.Unlock()
	instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "active", Active: true, SessionID: "session-2"})
	select {
	case <-newSessionStarted:
		t.Fatal("replacement route started before stale recovery completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseStop)
	deadline := time.Now().Add(time.Second)
	for {
		instance.reconcileVoiceVerificationAudio(ctx, callStateInfo{State: "active", Active: true, SessionID: "session-2"})
		select {
		case <-newSessionStarted:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement route did not start after stale recovery completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
