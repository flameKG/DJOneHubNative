package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	agentPCMMagic       = "DJA1"
	agentPCMVersion     = byte(1)
	agentPCMHeaderBytes = 32
	agentPCMNonceBytes  = 16
	agentPCMFrameBytes  = 320

	agentPCMHello  = byte(1)
	agentPCMAck    = byte(2)
	agentPCMModule = byte(4)
	agentPCMStop   = byte(5)
	agentPCMError  = byte(6)
)

// runAgentPCMClient connects the backend to the native App's owner-only PCM
// socket. The native App sends module-to-Mac PCM as type-4 frames.
func runAgentPCMClient(ctx context.Context, socketPath string, onModulePCM func([]byte)) error {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect Agent PCM: %w", err)
	}
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watchDone:
		}
	}()
	defer close(watchDone)
	defer conn.Close()

	zeroNonce := make([]byte, agentPCMNonceBytes)
	if err := writeAgentPCMFrame(conn, agentPCMHello, 0, zeroNonce, nil); err != nil {
		return fmt.Errorf("send Agent PCM hello: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	frameType, sequence, nonce, payload, err := readAgentPCMFrame(conn)
	if err != nil {
		return fmt.Errorf("read Agent PCM ack: %w", err)
	}
	if frameType != agentPCMAck || sequence != 0 || len(payload) != 0 || len(nonce) != agentPCMNonceBytes {
		return fmt.Errorf("invalid Agent PCM ack")
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}

	expectedSequence := uint32(1)
	for {
		frameType, sequence, frameNonce, payload, err := readAgentPCMFrame(conn)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read Agent PCM frame: %w", err)
		}
		if !equalAgentPCMNonce(frameNonce, nonce) {
			return fmt.Errorf("stale Agent PCM session")
		}
		if frameType == agentPCMError {
			return fmt.Errorf("Agent PCM server: %s", string(payload))
		}
		if frameType != agentPCMModule || sequence != expectedSequence || len(payload) != agentPCMFrameBytes {
			return fmt.Errorf("invalid Agent PCM module frame")
		}
		expectedSequence++
		if onModulePCM != nil {
			onModulePCM(payload)
		}
	}
}

func readAgentPCMFrame(reader io.Reader) (byte, uint32, []byte, []byte, error) {
	header := make([]byte, agentPCMHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, 0, nil, nil, err
	}
	if string(header[:4]) != agentPCMMagic || header[4] != agentPCMVersion {
		return 0, 0, nil, nil, errors.New("invalid Agent PCM header")
	}
	payloadSize := binary.BigEndian.Uint32(header[12:16])
	if payloadSize > 4096 {
		return 0, 0, nil, nil, errors.New("Agent PCM payload too large")
	}
	payload := make([]byte, int(payloadSize))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, 0, nil, nil, err
	}
	return header[5], binary.BigEndian.Uint32(header[8:12]), append([]byte(nil), header[16:32]...), payload, nil
}

func writeAgentPCMFrame(writer io.Writer, frameType byte, sequence uint32, nonce, payload []byte) error {
	if len(nonce) != agentPCMNonceBytes || len(payload) > 4096 {
		return errors.New("invalid Agent PCM frame")
	}
	frame := make([]byte, agentPCMHeaderBytes+len(payload))
	copy(frame[:4], agentPCMMagic)
	frame[4] = agentPCMVersion
	frame[5] = frameType
	binary.BigEndian.PutUint32(frame[8:12], sequence)
	binary.BigEndian.PutUint32(frame[12:16], uint32(len(payload)))
	copy(frame[16:32], nonce)
	copy(frame[32:], payload)
	for len(frame) > 0 {
		written, err := writer.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func equalAgentPCMNonce(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}
