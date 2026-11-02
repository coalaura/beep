//go:build windows

package main

import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type tWAVEFORMATEX struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

type tWAVEHDR struct {
	lpData          *float32
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwLoops         uint32
	lpNext          *tWAVEHDR
	reserved        uintptr
}

var (
	winmm = syscall.NewLazyDLL("winmm.dll")

	waveOutOpen            = winmm.NewProc("waveOutOpen")
	waveOutPrepareHeader   = winmm.NewProc("waveOutPrepareHeader")
	waveOutWrite           = winmm.NewProc("waveOutWrite")
	waveOutUnprepareHeader = winmm.NewProc("waveOutUnprepareHeader")
	waveOutReset           = winmm.NewProc("waveOutReset")
	waveOutClose           = winmm.NewProc("waveOutClose")
)

const (
	WAVE_FORMAT_IEEE_FLOAT = 0x0003

	WAVE_MAPPER    = 0xffffffff
	CALLBACK_EVENT = 0x00050000
	WHDR_DONE      = 0x00000001
)

func beep() (err error) {
	var waveFormat tWAVEFORMATEX

	waveFormat.cbSize = 0
	waveFormat.wFormatTag = WAVE_FORMAT_IEEE_FLOAT
	waveFormat.nChannels = 2
	waveFormat.nSamplesPerSec = 48000

	nFrames := int(chimeDuration * float64(waveFormat.nSamplesPerSec))
	if nFrames < 1 {
		return errors.New("duration must be at least one sample frame")
	}

	nBuffer := nFrames * int(waveFormat.nChannels)

	waveFormat.wBitsPerSample = 32                    // 4-byte float = 32 bits
	waveFormat.nBlockAlign = waveFormat.nChannels * 4 // 4-byte float = 4 bytes

	waveFormat.nAvgBytesPerSec = waveFormat.nSamplesPerSec * uint32(waveFormat.nBlockAlign)

	buffer := make([]float32, nBuffer)

	for i := 0; i < nBuffer; i += int(waveFormat.nChannels) {
		for j := (0); j < int(waveFormat.nChannels); j++ {
			timeInSeconds := float32(i/int(waveFormat.nChannels)) / float32(waveFormat.nSamplesPerSec)

			buffer[i+j] = chimeVolume * chime(timeInSeconds, j)
		}
	}

	cleanup := func(operation string, proc *syscall.LazyProc, args ...uintptr) {
		var cleanupErr error

		for {
			result, _, _ := proc.Call(args...)
			if result == 0 {
				err = errors.Join(err, cleanupErr)

				return
			}

			if cleanupErr == nil {
				cleanupErr = fmt.Errorf("%s: MMRESULT %d", operation, result)
			}

			time.Sleep(time.Millisecond)
		}
	}

	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return fmt.Errorf("create playback event: %w", err)
	}

	defer func() {
		closeErr := windows.CloseHandle(event)
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close playback event: %w", closeErr))
		}
	}()

	var hWavOut uintptr

	result, _, _ := waveOutOpen.Call(
		uintptr(unsafe.Pointer(&hWavOut)),
		WAVE_MAPPER,
		uintptr(unsafe.Pointer(&waveFormat)),
		uintptr(event),
		0,
		CALLBACK_EVENT,
	)

	if result != 0 {
		return fmt.Errorf("waveOutOpen: MMRESULT %d", result)
	}

	defer cleanup("waveOutClose", waveOutClose, hWavOut)

	hdr := tWAVEHDR{
		lpData:         &buffer[0],
		dwBufferLength: uint32(len(buffer) * 4),
	}

	var pinner runtime.Pinner

	pinner.Pin(&buffer[0])
	pinner.Pin(&hdr)

	defer pinner.Unpin()

	result, _, _ = waveOutPrepareHeader.Call(
		hWavOut,
		uintptr(unsafe.Pointer(&hdr)),
		unsafe.Sizeof(hdr),
	)

	if result != 0 {
		return fmt.Errorf("waveOutPrepareHeader: MMRESULT %d", result)
	}

	defer cleanup("waveOutUnprepareHeader", waveOutUnprepareHeader, hWavOut, uintptr(unsafe.Pointer(&hdr)), unsafe.Sizeof(hdr))

	result, _, _ = waveOutWrite.Call(
		hWavOut,
		uintptr(unsafe.Pointer(&hdr)),
		unsafe.Sizeof(hdr),
	)

	if result != 0 {
		return fmt.Errorf("waveOutWrite: MMRESULT %d", result)
	}

	defer func() {
		if atomic.LoadUint32(&hdr.dwFlags)&WHDR_DONE == 0 {
			cleanup("waveOutReset", waveOutReset, hWavOut)
		}
	}()

	for atomic.LoadUint32(&hdr.dwFlags)&WHDR_DONE == 0 {
		status, err := windows.WaitForSingleObject(event, windows.INFINITE)
		if err != nil {
			return fmt.Errorf("wait for playback: %w", err)
		}

		if status != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("wait for playback: unexpected status %#x", status)
		}
	}

	return nil
}
