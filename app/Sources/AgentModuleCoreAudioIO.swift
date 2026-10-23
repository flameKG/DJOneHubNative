import Foundation
import CoreAudio
import CryptoKit

struct AgentModuleAudioDeviceInfo: Equatable {
    let uidHash: String
    let name: String
    let inputChannels: UInt32
    let outputChannels: UInt32
    let sampleRate: Double
    let bitsPerChannel: UInt32
    let isPCM: Bool
    let likelyModule: Bool
}

enum AgentModuleCoreAudioDiscovery {
    static func list() -> [AgentModuleAudioDeviceInfo] {
        deviceIDs().compactMap { inspect($0) }.sorted {
            $0.name.localizedStandardCompare($1.name) == .orderedAscending
        }
    }

    static func likelyModuleDevices() -> [AgentModuleAudioDeviceInfo] {
        list().filter(\.likelyModule)
    }

    private static func inspect(_ id: AudioDeviceID) -> AgentModuleAudioDeviceInfo? {
        guard uintProperty(id, kAudioDevicePropertyDeviceIsAlive) == 1,
              uintProperty(id, kAudioDevicePropertyIsHidden) != 1 else { return nil }
        let name = stringProperty(id, kAudioObjectPropertyName)
        let uid = stringProperty(id, kAudioDevicePropertyDeviceUID)
        guard !name.isEmpty, !uid.isEmpty else { return nil }
        let input = streamInfo(id, scope: kAudioObjectPropertyScopeInput)
        let output = streamInfo(id, scope: kAudioObjectPropertyScopeOutput)
        guard input.channels > 0 || output.channels > 0 else { return nil }
        let identity = "\(uid) \(name)".lowercased()
        let likelyModule = ["ac interface", "as interface", "baiwang", "百旺", "quectel", "qdc507"]
            .contains { identity.contains($0) }
        let format = input.sampleRate > 0 ? input : output
        return AgentModuleAudioDeviceInfo(
            uidHash: hash(uid), name: name,
            inputChannels: input.channels, outputChannels: output.channels,
            sampleRate: format.sampleRate, bitsPerChannel: format.bits,
            isPCM: format.isPCM, likelyModule: likelyModule
        )
    }

    private struct StreamInfo {
        var channels: UInt32 = 0
        var sampleRate: Double = 0
        var bits: UInt32 = 0
        var isPCM = false
    }

    private static func streamInfo(_ id: AudioDeviceID, scope: AudioObjectPropertyScope) -> StreamInfo {
        var result = StreamInfo()
        var configAddress = address(kAudioDevicePropertyStreamConfiguration, scope: scope)
        var size: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(id, &configAddress, 0, nil, &size) == noErr,
              size >= MemoryLayout<AudioBufferList>.size else { return result }
        let memory = UnsafeMutableRawPointer.allocate(byteCount: Int(size), alignment: MemoryLayout<AudioBufferList>.alignment)
        defer { memory.deallocate() }
        guard AudioObjectGetPropertyData(id, &configAddress, 0, nil, &size, memory) == noErr else { return result }
        let buffers = UnsafeMutableAudioBufferListPointer(memory.assumingMemoryBound(to: AudioBufferList.self))
        result.channels = buffers.reduce(0) { $0 + $1.mNumberChannels }
        guard result.channels > 0 else { return result }

        var formatAddress = address(kAudioDevicePropertyStreamFormat, scope: scope)
        var format = AudioStreamBasicDescription()
        var formatSize = UInt32(MemoryLayout<AudioStreamBasicDescription>.size)
        guard AudioObjectGetPropertyData(id, &formatAddress, 0, nil, &formatSize, &format) == noErr else { return result }
        result.sampleRate = format.mSampleRate
        result.bits = format.mBitsPerChannel
        let floatPCM = format.mFormatID == kAudioFormatLinearPCM && format.mFormatFlags & kLinearPCMFormatFlagIsFloat != 0
        let signedPCM = format.mFormatID == kAudioFormatLinearPCM && format.mFormatFlags & kLinearPCMFormatFlagIsSignedInteger != 0
        result.isPCM = (floatPCM || signedPCM) && format.mSampleRate > 0 && format.mBitsPerChannel > 0
        return result
    }

    private static func deviceIDs() -> [AudioDeviceID] {
        let system = AudioObjectID(kAudioObjectSystemObject)
        var property = address(kAudioHardwarePropertyDevices)
        var size: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(system, &property, 0, nil, &size) == noErr else { return [] }
        var ids = [AudioDeviceID](repeating: 0, count: Int(size) / MemoryLayout<AudioDeviceID>.size)
        guard AudioObjectGetPropertyData(system, &property, 0, nil, &size, &ids) == noErr else { return [] }
        return ids
    }

    private static func address(_ selector: AudioObjectPropertySelector,
                               scope: AudioObjectPropertyScope = kAudioObjectPropertyScopeGlobal) -> AudioObjectPropertyAddress {
        AudioObjectPropertyAddress(mSelector: selector, mScope: scope, mElement: kAudioObjectPropertyElementMain)
    }

    private static func stringProperty(_ id: AudioObjectID, _ selector: AudioObjectPropertySelector) -> String {
        var property = address(selector)
        var value: Unmanaged<CFString>?
        var size = UInt32(MemoryLayout<Unmanaged<CFString>?>.size)
        guard AudioObjectGetPropertyData(id, &property, 0, nil, &size, &value) == noErr,
              let value else { return "" }
        return value.takeRetainedValue() as String
    }

    private static func uintProperty(_ id: AudioObjectID, _ selector: AudioObjectPropertySelector) -> UInt32? {
        var property = address(selector)
        var value: UInt32 = 0
        var size = UInt32(MemoryLayout<UInt32>.size)
        guard AudioObjectGetPropertyData(id, &property, 0, nil, &size, &value) == noErr else { return nil }
        return value
    }

    private static func hash(_ value: String) -> String {
        SHA256.hash(data: Data(value.utf8)).map { String(format: "%02x", $0) }.joined()
    }
}
