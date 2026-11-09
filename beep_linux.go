//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	pcmSampleRate    = 48000
	pcmChannels      = 2
	pcmBytesPerFrame = pcmChannels * 2

	pcmAccessRWInterleaved = 3
	pcmFormatS16LE         = 2
	pcmSubformatStandard   = 0

	pcmParamAccess     = 0
	pcmParamFormat     = 1
	pcmParamSubformat  = 2
	pcmParamSampleBits = 8
	pcmParamFrameBits  = 9
	pcmParamChannels   = 10
	pcmParamRate       = 11
	pcmParamPeriodSize = 13
	pcmParamPeriods    = 15
	pcmParamBufferSize = 17

	pcmPeriodSize = 1024
	pcmPeriods    = 4
	pcmBufferSize = pcmPeriodSize * pcmPeriods

	pcmIoctlHWParams = (3 << 30) | (unsafe.Sizeof(tPCMHWParams{}) << 16) | ('A' << 8) | 0x11
	pcmIoctlSWParams = (3 << 30) | (unsafe.Sizeof(tPCMSWParams{}) << 16) | ('A' << 8) | 0x13
	pcmIoctlPrepare  = ('A' << 8) | 0x40
	pcmIoctlDrain    = ('A' << 8) | 0x44
)

type tPCMMask struct {
	bits [8]uint32
}

type tPCMInterval struct {
	min   uint32
	max   uint32
	flags uint32
}

type tPCMHWParams struct {
	flags     uint32
	masks     [3]tPCMMask
	mres      [5]tPCMMask
	intervals [12]tPCMInterval
	ires      [9]tPCMInterval
	rmask     uint32
	cmask     uint32
	info      uint32
	msbits    uint32
	rateNum   uint32
	rateDen   uint32
	fifoSize  uintptr
	reserved  [64]byte
}

type tPCMSWParams struct {
	tstampMode       int32
	periodStep       uint32
	sleepMin         uint32
	availMin         uintptr
	xferAlign        uintptr
	startThreshold   uintptr
	stopThreshold    uintptr
	silenceThreshold uintptr
	silenceSize      uintptr
	boundary         uintptr
	proto            uint32
	tstampType       uint32
	reserved         [56]byte
}

func pcmIoctl(fd int, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		request,
		uintptr(arg),
	)

	if errno != 0 {
		return errno
	}

	return nil
}

func pcmSetMask(params *tPCMHWParams, param, value int) {
	params.masks[param] = tPCMMask{}
	params.masks[param].bits[value/32] = 1 << (value % 32)
}

func pcmSetInterval(params *tPCMHWParams, param int, value uint32) {
	params.intervals[param-pcmParamSampleBits] = tPCMInterval{
		min:   value,
		max:   value,
		flags: 1 << 2, // Integer interval.
	}
}

func beep() (err error) {
	nFramesFloat := chimeDuration * float64(pcmSampleRate)
	if math.IsNaN(nFramesFloat) || nFramesFloat < 1 {
		return errors.New("duration must be at least one sample frame")
	}

	if nFramesFloat >= float64(int(^uint(0)>>1)/pcmBytesPerFrame) {
		return errors.New("duration is too large")
	}

	nFrames := int(nFramesFloat)
	buffer := make([]byte, nFrames*pcmBytesPerFrame)

	for i := range nFrames {
		timeInSeconds := float32(i) / float32(pcmSampleRate)

		for j := range pcmChannels {
			sample := chimeVolume * chime(timeInSeconds, j)
			if sample > 1 {
				sample = 1
			} else if sample < -1 {
				sample = -1
			}

			offset := i*pcmBytesPerFrame + j*2
			binary.LittleEndian.PutUint16(buffer[offset:], uint16(int16(sample*32767)))
		}
	}

	available, serverErr := playPulse(buffer)
	if available {
		return serverErr
	}

	err = playALSA(buffer)
	if err != nil {
		return errors.Join(serverErr, err)
	}

	return nil
}

func playALSA(buffer []byte) (err error) {
	fd, err := openPlaybackDevice()
	if err != nil {
		return err
	}

	defer func() {
		closeErr := syscall.Close(fd)
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close playback device: %w", closeErr))
		}
	}()

	return writePlayback(fd, buffer)
}

func preparePlaybackDevice(fd int) error {
	err := syscall.SetNonblock(fd, false)
	if err != nil {
		return fmt.Errorf("set blocking playback: %w", err)
	}

	var hwParams tPCMHWParams

	for i := range hwParams.masks {
		for j := range hwParams.masks[i].bits {
			hwParams.masks[i].bits[j] = ^uint32(0)
		}
	}

	for i := range hwParams.intervals {
		hwParams.intervals[i].max = ^uint32(0)
	}

	hwParams.rmask = ^uint32(0)

	pcmSetMask(&hwParams, pcmParamAccess, pcmAccessRWInterleaved)
	pcmSetMask(&hwParams, pcmParamFormat, pcmFormatS16LE)
	pcmSetMask(&hwParams, pcmParamSubformat, pcmSubformatStandard)
	pcmSetInterval(&hwParams, pcmParamSampleBits, 16)
	pcmSetInterval(&hwParams, pcmParamFrameBits, pcmBytesPerFrame*8)
	pcmSetInterval(&hwParams, pcmParamChannels, pcmChannels)
	pcmSetInterval(&hwParams, pcmParamRate, pcmSampleRate)
	pcmSetInterval(&hwParams, pcmParamPeriodSize, pcmPeriodSize)
	pcmSetInterval(&hwParams, pcmParamPeriods, pcmPeriods)
	pcmSetInterval(&hwParams, pcmParamBufferSize, pcmBufferSize)

	err = pcmIoctl(fd, pcmIoctlHWParams, unsafe.Pointer(&hwParams))
	if err != nil {
		return fmt.Errorf("configure playback hardware: %w", err)
	}

	// ALSA uses a large multiple of the buffer size for wrapping frame counters.
	boundary := uintptr(pcmBufferSize)
	maxInt := ^uintptr(0) >> 1

	for boundary <= (maxInt-pcmBufferSize)/2 {
		boundary *= 2
	}

	swParams := tPCMSWParams{
		periodStep:     1,
		availMin:       1,
		xferAlign:      1,
		startThreshold: 1,
		stopThreshold:  pcmBufferSize,
		boundary:       boundary,
	}

	err = pcmIoctl(fd, pcmIoctlSWParams, unsafe.Pointer(&swParams))
	if err != nil {
		return fmt.Errorf("configure playback software: %w", err)
	}

	err = pcmIoctl(fd, pcmIoctlPrepare, nil)
	if err != nil {
		return fmt.Errorf("prepare playback: %w", err)
	}

	return nil
}

func writePlayback(fd int, buffer []byte) error {
	for offset := 0; offset < len(buffer); {
		n, writeErr := syscall.Write(fd, buffer[offset:])
		if errors.Is(writeErr, syscall.EINTR) {
			continue
		}

		if writeErr != nil {
			return fmt.Errorf("write playback samples: %w", writeErr)
		}

		if n <= 0 || n%pcmBytesPerFrame != 0 {
			return fmt.Errorf("write playback samples: unexpected byte count %d", n)
		}

		offset += n
	}

	err := pcmIoctl(fd, pcmIoctlDrain, nil)
	if err != nil {
		return fmt.Errorf("wait for playback: %w", err)
	}

	return nil
}

func openPlaybackDevice() (int, error) {
	devices, err := filepath.Glob("/dev/snd/pcmC*D*p")
	if err != nil {
		return -1, fmt.Errorf("find playback devices: %w", err)
	}

	if len(devices) == 0 {
		return -1, errors.New("no ALSA playback devices found")
	}

	var deviceErr error

	for _, device := range devices {
		// Open nonblocking so an already-busy device fails instead of hanging.
		fd, err := syscall.Open(device, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
		if err != nil {
			deviceErr = errors.Join(deviceErr, fmt.Errorf("open playback device %s: %w", device, err))

			continue
		}

		err = preparePlaybackDevice(fd)
		if err != nil {
			deviceErr = errors.Join(deviceErr, fmt.Errorf("prepare playback device %s: %w", device, err))

			closeErr := syscall.Close(fd)
			if closeErr != nil {
				return -1, errors.Join(deviceErr, fmt.Errorf("close playback device %s: %w", device, closeErr))
			}

			continue
		}

		return fd, nil
	}

	return -1, fmt.Errorf("no usable ALSA playback device: %w", deviceErr)
}
