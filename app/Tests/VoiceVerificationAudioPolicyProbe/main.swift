import Foundation

@main
struct VoiceVerificationAudioPolicyProbe {
    static func main() {
        precondition(VoiceVerificationAudioPolicy.reservesAgentAudio(
            callSessionID: "session-a",
            callActive: true,
            verification: VoiceVerificationAudioStatus(
                answered: true,
                consumed: true,
                audioAuthorized: true,
                sessionID: "session-a"
            )
        ))
        precondition(!VoiceVerificationAudioPolicy.reservesAgentAudio(
            callSessionID: "session-b",
            callActive: true,
            verification: VoiceVerificationAudioStatus(
                answered: true,
                consumed: true,
                audioAuthorized: true,
                sessionID: "session-a"
            )
        ))
        precondition(!VoiceVerificationAudioPolicy.reservesAgentAudio(
            callSessionID: "session-a",
            callActive: false,
            verification: VoiceVerificationAudioStatus(
                answered: true,
                consumed: true,
                audioAuthorized: true,
                sessionID: "session-a"
            )
        ))
        precondition(!VoiceVerificationAudioPolicy.reservesAgentAudio(
            callSessionID: "session-a",
            callActive: true,
            verification: VoiceVerificationAudioStatus(
                answered: true,
                consumed: true,
                audioAuthorized: false,
                sessionID: "session-a"
            )
        ))
        print("PASS voice-verification Agent audio reservation policy")
    }
}
