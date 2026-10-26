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

func TestVoiceVerificationWaitsForNativeModuleIOStopBeforeRouteTeardown(t *testing.T) {
	routeStopped := make(chan struct{}, 1)
	instance := &app{
		moduleVoiceAutoStart: true,
		moduleVoiceRouteStop: func(string) error {
			routeStopped <- struct{}{}
			return nil
		},
	}
	instance.moduleVoiceRoute = true
	instance.agentAudio = agentAudioState{
		Compiled: true, TransportReady: true, ClientConnected: true, ModuleIOReady: true,
	}

	instance.reconcileVoiceVerificationAudio(context.Background(), callStateInfo{State: "idle"})
	select {
	case <-routeStopped:
		t.Fatal("module voice route stopped before native CoreAudio teardown completed")
	case <-time.After(100 * time.Millisecond):
	}

	instance.agentAudioMu.Lock()
	instance.agentAudio.ClientConnected = false
	instance.agentAudio.ModuleIOReady = false
	instance.agentAudioMu.Unlock()
	instance.reconcileVoiceVerificationAudio(context.Background(), callStateInfo{State: "idle"})
	select {
	case <-routeStopped:
	case <-time.After(time.Second):
		t.Fatal("module voice route was not stopped after native CoreAudio teardown")
	}
}

func TestStopModuleVoiceRouteSafelyBoundsStaleNativeAudioState(t *testing.T) {
	var routeStops atomic.Int32
	instance := &app{
		moduleVoiceAutoStart: true,
		moduleVoiceRouteStop: func(string) error {
			routeStops.Add(1)
			return nil
		},
	}
	instance.moduleVoiceRoute = true
	instance.agentAudio = agentAudioState{ClientConnected: true, ModuleIOReady: true}

	started := time.Now()
	err := instance.stopModuleVoiceRouteSafely(25 * time.Millisecond)
	if err == nil {
		t.Fatal("expected stale native audio state to block route teardown")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded teardown took too long: %s", elapsed)
	}
	if routeStops.Load() != 0 {
		t.Fatalf("route stops=%d want=0 while native audio state is unknown", routeStops.Load())
	}
}

func TestStopModuleVoiceRouteSafelyStopsManualRoute(t *testing.T) {
	var routeStops atomic.Int32
	instance := &app{
		moduleVoiceAutoStart: false,
		moduleVoiceRouteStop: func(string) error {
			routeStops.Add(1)
			return nil
		},
	}
	instance.moduleVoiceRoute = true

	if err := instance.stopModuleVoiceRouteSafely(time.Second); err != nil {
		t.Fatalf("safe route stop failed: %v", err)
	}
	if routeStops.Load() != 1 {
		t.Fatalf("route stops=%d want=1", routeStops.Load())
	}
}

func TestSafeRouteTeardownBlocksAuthorizedReconcileUntilNativeStops(t *testing.T) {
	agentStarted := make(chan struct{}, 1)
	instance := &app{moduleVoiceAutoStart: true}
	instance.moduleVoiceRoute = true
	instance.agentAudio = agentAudioState{
		Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock",
		ClientConnected: true, ModuleIOReady: true,
	}
	instance.agentPCMRunner = func(context.Context, string, func([]byte)) error {
		agentStarted <- struct{}{}
		return nil
	}
	instance.voiceVerification = &voiceVerificationWindow{
		Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-2",
	}

	teardownDone := make(chan error, 1)
	go func() { teardownDone <- instance.stopModuleVoiceRouteSafely(250 * time.Millisecond) }()
	deadline := time.Now().Add(time.Second)
	for {
		instance.moduleVoiceMu.Lock()
		tearingDown := instance.moduleVoiceTeardownInProgress
		instance.moduleVoiceMu.Unlock()
		if tearingDown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("safe teardown did not publish its transition barrier")
		}
		time.Sleep(time.Millisecond)
	}

	instance.reconcileVoiceVerificationAudio(context.Background(), callStateInfo{State: "active", Active: true, SessionID: "session-2"})
	select {
	case <-agentStarted:
		t.Fatal("authorized reconciliation restarted PCM during route teardown")
	case <-time.After(50 * time.Millisecond):
	}
	if err := <-teardownDone; err == nil {
		t.Fatal("expected bounded teardown to time out while native state remained active")
	}
}

func TestStaleRouteStartCannotCommitAfterTeardown(t *testing.T) {
	settleEntered := make(chan struct{})
	releaseSettle := make(chan struct{})
	instance := &app{
		moduleVoiceAutoStart:  true,
		moduleVoiceRouteStart: func(string) error { return nil },
		moduleVoiceRouteSettle: func() {
			close(settleEntered)
			<-releaseSettle
		},
	}

	startDone := make(chan error, 1)
	go func() { startDone <- instance.ensureModuleVoiceRoute("old-session") }()
	select {
	case <-settleEntered:
	case <-time.After(time.Second):
		t.Fatal("route start did not enter settle window")
	}
	if err := instance.stopModuleVoiceRouteSafely(time.Second); err != nil {
		t.Fatalf("safe teardown failed during stale start: %v", err)
	}
	close(releaseSettle)
	if err := <-startDone; !errors.Is(err, errModuleVoiceTeardownInProgress) {
		t.Fatalf("stale route start error=%v want teardown transition error", err)
	}
	instance.moduleVoiceMu.Lock()
	active := instance.moduleVoiceRoute
	instance.moduleVoiceMu.Unlock()
	if active {
		t.Fatal("stale route start resurrected a route after teardown")
	}
}

func TestStaleRouteStartCannotCommitAfterModuleReset(t *testing.T) {
	settleEntered := make(chan struct{})
	releaseSettle := make(chan struct{})
	instance := &app{
		moduleVoiceAutoStart:  true,
		moduleVoiceRouteStart: func(string) error { return nil },
		moduleVoiceRouteSettle: func() {
			close(settleEntered)
			<-releaseSettle
		},
	}
	startDone := make(chan error, 1)
	go func() { startDone <- instance.ensureModuleVoiceRoute("old-session") }()
	<-settleEntered
	instance.resetModuleVoiceState()
	close(releaseSettle)
	if err := <-startDone; !errors.Is(err, errModuleVoiceTeardownInProgress) {
		t.Fatalf("stale route start error=%v want transition invalidation", err)
	}
	instance.moduleVoiceMu.Lock()
	active := instance.moduleVoiceRoute
	instance.moduleVoiceMu.Unlock()
	if active {
		t.Fatal("stale route start resurrected route after module reset")
	}
}

func TestAuthorizedReconcileResumesTimedOutTeardownAfterNativeStops(t *testing.T) {
	var routeStarts atomic.Int32
	var routeStops atomic.Int32
	agentStarted := make(chan struct{}, 1)
	instance := &app{
		moduleVoiceAutoStart: true,
		moduleVoiceRouteStart: func(string) error {
			routeStarts.Add(1)
			return nil
		},
		moduleVoiceRouteStop: func(string) error {
			routeStops.Add(1)
			return nil
		},
	}
	instance.moduleVoiceRoute = true
	instance.agentAudio = agentAudioState{
		Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock",
		ClientConnected: true, ModuleIOReady: true,
	}
	instance.agentPCMRunner = func(context.Context, string, func([]byte)) error {
		agentStarted <- struct{}{}
		return nil
	}
	instance.voiceVerification = &voiceVerificationWindow{
		Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-3",
	}
	if err := instance.stopModuleVoiceRouteSafely(20 * time.Millisecond); err == nil {
		t.Fatal("expected initial native stop timeout")
	}
	instance.agentAudioMu.Lock()
	instance.agentAudio.ClientConnected = false
	instance.agentAudio.ModuleIOReady = false
	instance.agentAudioMu.Unlock()

	instance.reconcileVoiceVerificationAudio(context.Background(), callStateInfo{State: "active", Active: true, SessionID: "session-3"})
	select {
	case <-agentStarted:
	case <-time.After(time.Second):
		t.Fatal("authorized reconciliation did not recover after native stop became observable")
	}
	if routeStops.Load() != 1 || routeStarts.Load() != 1 {
		t.Fatalf("route transitions stop=%d start=%d want=1/1", routeStops.Load(), routeStarts.Load())
	}
}

func TestAuthorizedReconcilePreservesRecoveryExhaustionWhenPendingTeardownCompletes(t *testing.T) {
	var routeStarts atomic.Int32
	var pcmStarts atomic.Int32
	instance := &app{
		moduleVoiceAutoStart: true,
		moduleVoiceRouteStart: func(string) error {
			routeStarts.Add(1)
			return nil
		},
		moduleVoiceRouteStop: func(string) error { return nil },
	}
	instance.moduleVoiceRoute = true
	instance.moduleVoiceTeardownInProgress = true
	instance.moduleVoiceRecoveryExhausted = "session-exhausted"
	instance.agentAudio = agentAudioState{Compiled: true, TransportReady: true, SocketPath: "/private/tmp/agent.sock"}
	instance.agentPCMRunner = func(context.Context, string, func([]byte)) error {
		pcmStarts.Add(1)
		return nil
	}
	instance.voiceVerification = &voiceVerificationWindow{
		Answered: true, Consumed: true, AudioAuthorized: true, SessionID: "session-exhausted",
	}
	call := callStateInfo{State: "active", Active: true, SessionID: "session-exhausted"}
	for range 3 {
		instance.reconcileVoiceVerificationAudio(context.Background(), call)
	}
	instance.moduleVoiceMu.Lock()
	exhausted := instance.moduleVoiceRecoveryExhausted
	instance.moduleVoiceMu.Unlock()
	if exhausted != "session-exhausted" {
		t.Fatalf("recovery exhausted state=%q want preserved session", exhausted)
	}
	if routeStarts.Load() != 0 || pcmStarts.Load() != 0 {
		t.Fatalf("exhausted recovery restarted route/PCM: route=%d pcm=%d", routeStarts.Load(), pcmStarts.Load())
	}
}
