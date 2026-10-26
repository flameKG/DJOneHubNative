package main

import (
	"context"
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
