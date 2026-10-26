package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentAudioTranscriptStoresAndExtractsCodeThenClearsOnStop(t *testing.T) {
	instance := &app{}
	handler := instance.routes()
	t.Setenv(appTokenEnvironment, "native-app-transcript-test-token")

	request := httptest.NewRequest(http.MethodPut, "/api/agent/audio/transcript", bytes.NewBufferString(`{"text":"验证码是 一 二 三 四","final":true}`))
	request.Header.Set(appTokenHeader, "native-app-transcript-test-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("transcript status=%d body=%s", response.Code, response.Body.String())
	}
	var state agentAudioState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Transcript != "验证码是 一 二 三 四" || !state.TranscriptFinal || state.VerificationCode != "1234" {
		t.Fatalf("unexpected transcript state=%+v", state)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/agent/audio/stop", nil)
	response = httptest.NewRecorder()
	instance.agentAudioStop(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", response.Code, response.Body.String())
	}
	state = agentAudioState{}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Transcript != "" || state.VerificationCode != "" {
		t.Fatalf("transcript not cleared: %+v", state)
	}
}
