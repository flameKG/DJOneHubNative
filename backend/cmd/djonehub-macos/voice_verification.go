package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type voiceVerificationWindow struct {
	Active          bool      `json:"active"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	AllowedNumbers  []string  `json:"allowed_numbers"`
	Answered        bool      `json:"answered"`
	SessionID       string    `json:"session_id,omitempty"`
	Consumed        bool      `json:"consumed"`
	AudioAuthorized bool      `json:"audio_authorized"`
	LastError       string    `json:"last_error,omitempty"`
}

func (a *app) armVoiceVerification(duration time.Duration, allowed []string) voiceVerificationWindow {
	a.stopVoiceVerificationAudio()
	now := time.Now()
	window := voiceVerificationWindow{
		Active:         true,
		ExpiresAt:      now.Add(duration),
		AllowedNumbers: append([]string(nil), allowed...),
	}
	a.voiceVerificationMu.Lock()
	a.voiceVerification = &window
	a.voiceVerificationMu.Unlock()
	return window
}

func (a *app) voiceVerificationSnapshot() voiceVerificationWindow {
	a.voiceVerificationMu.Lock()
	defer a.voiceVerificationMu.Unlock()
	if a.voiceVerification == nil {
		return voiceVerificationWindow{}
	}
	window := *a.voiceVerification
	window.AllowedNumbers = append([]string(nil), a.voiceVerification.AllowedNumbers...)
	if window.Active && !window.ExpiresAt.IsZero() && time.Now().After(window.ExpiresAt) {
		window.Active = false
	}
	return window
}

func (a *app) disarmVoiceVerification() {
	a.voiceVerificationMu.Lock()
	if a.voiceVerification != nil {
		a.voiceVerification.Active = false
		a.voiceVerification.AudioAuthorized = false
	}
	a.voiceVerificationMu.Unlock()
	a.stopVoiceVerificationAudio()
}

func (a *app) updateVoiceVerification(update func(*voiceVerificationWindow)) {
	a.voiceVerificationMu.Lock()
	defer a.voiceVerificationMu.Unlock()
	if a.voiceVerification != nil {
		update(a.voiceVerification)
	}
}

func (a *app) voiceVerificationNumberAllowed(number string) bool {
	number = strings.TrimSpace(number)
	if number == "" {
		return false
	}
	window := a.voiceVerificationSnapshot()
	for _, allowed := range window.AllowedNumbers {
		if number == allowed {
			return true
		}
	}
	return false
}

func (a *app) runVoiceVerificationAutomation(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	defer a.stopVoiceVerificationAudio()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state := a.currentCallState()
			a.reconcileVoiceVerificationAudio(ctx, state)
			window := a.voiceVerificationSnapshot()
			if !window.Active {
				continue
			}
			if state.State != "incoming" || !a.voiceVerificationNumberAllowed(state.Number) {
				continue
			}
			if err := a.answerCurrentCall(); err != nil {
				a.updateVoiceVerification(func(current *voiceVerificationWindow) {
					current.LastError = err.Error()
				})
				continue
			}
			a.updateVoiceVerification(func(current *voiceVerificationWindow) {
				current.Active = false
				current.Answered = true
				current.Consumed = true
				current.AudioAuthorized = true
				current.SessionID = state.SessionID
				current.LastError = ""
			})
		}
	}
}

func (a *app) answerCurrentCall() error {
	a.callControlMu.Lock()
	defer a.callControlMu.Unlock()
	if state := a.queryCallState().State; state != "incoming" {
		return &voiceVerificationError{message: "当前通话状态为 " + state}
	}
	if err := a.callModeReadyForCall(); err != nil {
		return err
	}
	response, err := a.runCallCommand("ATA", "", 10*time.Second)
	if !a.callModeATCommandAccepted(response, err) {
		return &voiceVerificationError{message: "模块未确认接听"}
	}
	a.markCallAnswered()
	return nil
}

type voiceVerificationError struct{ message string }

func (e *voiceVerificationError) Error() string { return e.message }

func (a *app) voiceVerificationArm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DurationSeconds int      `json:"duration_seconds"`
		AllowedNumbers  []string `json:"allowed_numbers"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.DurationSeconds < 30 || body.DurationSeconds > 900 {
		writeCodedError(w, http.StatusBadRequest, "invalid_duration", "接听窗口必须为 30 到 900 秒", false)
		return
	}
	if len(body.AllowedNumbers) == 0 || len(body.AllowedNumbers) > 10 {
		writeCodedError(w, http.StatusBadRequest, "invalid_allowed_numbers", "必须指定 1 到 10 个允许号码", false)
		return
	}
	allowed := make([]string, 0, len(body.AllowedNumbers))
	for _, number := range body.AllowedNumbers {
		number = strings.TrimSpace(number)
		if !validVoiceNumber(number) {
			writeCodedError(w, http.StatusBadRequest, "invalid_allowed_number", "允许号码格式无效", false)
			return
		}
		allowed = append(allowed, number)
	}
	writeJSON(w, http.StatusOK, a.armVoiceVerification(time.Duration(body.DurationSeconds)*time.Second, allowed))
}

func (a *app) voiceVerificationStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.voiceVerificationSnapshot())
}

func (a *app) voiceVerificationDisarm(w http.ResponseWriter, _ *http.Request) {
	a.disarmVoiceVerification()
	writeJSON(w, http.StatusOK, a.voiceVerificationSnapshot())
}
