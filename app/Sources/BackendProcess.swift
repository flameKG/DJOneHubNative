import Foundation
import Combine

/// 管理 Go 后端子进程的生命周期，并对外暴露 Unix socket 路径。
final class BackendProcess: ObservableObject {
    enum State: Equatable {
        case stopped
        case starting
        case running
        case failed(String)
    }

    static let shared = BackendProcess()

    @Published private(set) var state: State = .stopped
    @Published private(set) var childPID: pid_t = 0

    private var process: Process?
    private var stdoutTask: Task<Void, Never>?
    private var stderrTask: Task<Void, Never>?

    let socketPath: String
    let logURL: URL

    private init() {
        let fm = FileManager.default
        let support = fm.urls(for: .applicationSupportDirectory, in: .userDomainMask).first!
            .appendingPathComponent("DJOneHubNative", isDirectory: true)
        try? fm.createDirectory(at: support, withIntermediateDirectories: true)
        try? fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: support.path)
        socketPath = support.appendingPathComponent("djonehub.sock").path
        logURL = support.appendingPathComponent("djonehub.log")

        UnixSocketURLProtocol.socketPath = socketPath
    }

    var binaryURL: URL? {
        Bundle.main.url(forResource: "djonehubd", withExtension: nil, subdirectory: "backend")
    }

    func start() {
        if case .running = state, process?.isRunning == true { return }
        guard let binary = binaryURL else {
            state = .failed("缺少后端程序 djonehubd")
            return
        }

        try? FileManager.default.removeItem(atPath: socketPath)

        let proc = Process()
        proc.executableURL = binary
        proc.arguments = ["-listen", "unix:\(socketPath)"]
        var environment = ProcessInfo.processInfo.environment
        environment["DJONEHUB_APP_TOKEN"] = LocalBackendAuthentication.appToken
        proc.environment = environment

        let outPipe = Pipe()
        let errPipe = Pipe()
        proc.standardOutput = outPipe
        proc.standardError = errPipe
        proc.terminationHandler = { [weak self] p in
            Task { @MainActor in
                guard let self else { return }
                self.process = nil
                self.childPID = 0
                if self.state == .running || self.state == .starting {
                    self.state = .stopped
                }
            }
        }

        stdoutTask = Task.detached(priority: .utility) { [weak self] in
            self?.drain(outPipe.fileHandleForReading, to: .standardOutput)
        }
        stderrTask = Task.detached(priority: .utility) { [weak self] in
            self?.drain(errPipe.fileHandleForReading, to: .standardError)
        }

        do {
            try proc.run()
        } catch {
            state = .failed("无法启动后端：\(error.localizedDescription)")
            return
        }

        process = proc
        childPID = proc.processIdentifier
        state = .starting

        Task { @MainActor [weak self] in
            guard let self else { return }
            // 等待 socket 就绪
            for _ in 0..<50 {
                if FileManager.default.fileExists(atPath: self.socketPath) {
                    self.state = .running
                    self.startAgentAudioTransport()
                    return
                }
                if !proc.isRunning {
                    self.state = .failed("后端进程提前退出，请查看日志")
                    return
                }
                try? await Task.sleep(nanoseconds: 100_000_000)
            }
            self.state = .failed("后端启动超时")
        }
    }

    func stop() {
        AgentAudioBridge.shared.stop()
        if let proc = process, proc.isRunning {
            proc.terminate()
        }
        stdoutTask?.cancel()
        stderrTask?.cancel()
    }

    private func startAgentAudioTransport() {
        let bridge = AgentAudioBridge.shared
        bridge.onStateChange = { state in
            Task { await Self.reportAgentAudioState(state, socketPath: bridge.socketPath) }
        }
        do {
            try bridge.start()
        } catch {
            Task {
                await Self.reportAgentAudioState(
                    AgentAudioBridgeState(transportReady: false, clientConnected: false,
                                          sessionNonce: nil, lastError: error.localizedDescription),
                    socketPath: bridge.socketPath)
            }
        }
    }

    private struct AgentAudioTransportUpdate: Encodable {
        let compiled: Bool
        let transportReady: Bool
        let moduleIOReady: Bool
        let socketPath: String
        let clientConnected: Bool
        let error: String

        enum CodingKeys: String, CodingKey {
            case compiled
            case transportReady = "transport_ready"
            case moduleIOReady = "module_io_ready"
            case socketPath = "socket_path"
            case clientConnected = "client_connected"
            case error
        }
    }

    private struct AgentAudioTransportResponse: Decodable {}

    private static func reportAgentAudioState(_ state: AgentAudioBridgeState, socketPath: String) async {
        let update = AgentAudioTransportUpdate(
            compiled: true,
            transportReady: state.transportReady,
            moduleIOReady: state.moduleIOReady,
            socketPath: socketPath,
            clientConnected: state.clientConnected,
            error: state.lastError ?? "")

        // 后端 socket 文件出现后，HTTP 服务可能还没有完成监听。
        // 只尝试一次会把启动竞态永久固化为 compiled=false；状态注册必须可重试。
        for attempt in 0..<10 {
            do {
                let _: AgentAudioTransportResponse? = try await APIClient(timeoutInterval: 5).send(
                    "api/agent/audio/transport", method: "PUT", body: update)
                return
            } catch {
                guard attempt < 9 else { return }
                let delay = UInt64(100_000_000) * UInt64(attempt + 1)
                try? await Task.sleep(nanoseconds: delay)
            }
        }
    }

    private func drain(_ handle: FileHandle, to dest: FileHandle) {
        while true {
            let data = handle.availableData
            if data.isEmpty { break }
            try? dest.write(contentsOf: data)
        }
        try? handle.close()
    }
}
