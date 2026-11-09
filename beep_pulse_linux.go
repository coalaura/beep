package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/jfreymuth/pulse/proto"
)

func playPulse(buffer []byte) (bool, error) {
	client, connection, err := proto.Connect("")
	if err != nil {
		_, configured := os.LookupEnv("PULSE_SERVER")
		unavailable := errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)

		return configured || !unavailable, fmt.Errorf("connect to audio server: %w", err)
	}

	if connection == nil {
		return true, errors.New("no matching audio server")
	}

	defer connection.Close()

	// Allow for device wakeup and Bluetooth latency as well as the chime itself.
	timeout := time.Duration(chimeDuration*float64(time.Second)) + 5*time.Second
	client.SetTimeout(timeout)

	err = connection.SetDeadline(time.Now().Add(timeout))
	if err != nil {
		return true, fmt.Errorf("set audio server deadline: %w", err)
	}

	properties := proto.PropList{
		"application.name": proto.PropListString("beep"),
		"media.name":       proto.PropListString("Chime"),
	}

	err = client.Request(&proto.SetClientName{Props: properties}, &proto.SetClientNameReply{})
	if err != nil {
		return true, fmt.Errorf("identify audio client: %w", err)
	}

	request := proto.CreatePlaybackStream{
		Format:                proto.FormatInt16LE,
		Channels:              pcmChannels,
		Rate:                  pcmSampleRate,
		ChannelMap:            proto.ChannelMap{proto.ChannelLeft, proto.ChannelRight},
		SinkIndex:             proto.Undefined,
		BufferMaxLength:       uint32(len(buffer)),
		BufferTargetLength:    uint32(len(buffer)),
		BufferPrebufferLength: 0,
		BufferMinimumRequest:  proto.Undefined,
		Corked:                true,
		ChannelVolumes:        proto.ChannelVolumes{proto.VolumeNorm, proto.VolumeNorm},
		Properties:            properties,
	}

	var stream proto.CreatePlaybackStreamReply

	err = client.Request(&request, &stream)
	if err != nil {
		return true, fmt.Errorf("create audio playback stream: %w", err)
	}

	if uint64(len(buffer)) > uint64(stream.BufferMaxLength) {
		return true, errors.New("audio server playback buffer is too small")
	}

	// Queue the entire short chime before starting, then wait for the server to
	// finish playing it. Closing the connection earlier would truncate playback.
	err = client.Send(stream.StreamIndex, buffer)
	if err != nil {
		return true, fmt.Errorf("send audio samples: %w", err)
	}

	err = client.Request(&proto.CorkPlaybackStream{StreamIndex: stream.StreamIndex, Corked: false}, nil)
	if err != nil {
		return true, fmt.Errorf("start audio playback: %w", err)
	}

	err = client.Request(&proto.DrainPlaybackStream{StreamIndex: stream.StreamIndex}, nil)
	if err != nil {
		return true, fmt.Errorf("wait for audio playback: %w", err)
	}

	return true, nil
}
