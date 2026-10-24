package main

import (
	"testing"
	"time"
)

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
