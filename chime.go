package main

import "math"

const (
	chimeFrequency = 523.25 // C5
	chimeVolume    = 0.4

	chimeDuration = 0.3   // total length in seconds
	chimeAttack   = 0.004 // fade-in
	chimeRelease  = 0.03  // final fade
	chimeDecay    = 18.0  // exponential decay rate (1/s)
)

func chime(timeInSeconds float32, _ int) float32 {
	t := float64(timeInSeconds)
	if t < 0 || t >= chimeDuration {
		return 0
	}

	sample := 0.8 * chimeNote(t, float64(chimeFrequency))

	remaining := chimeDuration - t
	if remaining < chimeRelease {
		sample *= remaining / chimeRelease
	}

	return float32(0.9 * sample)
}

func chimeNote(t, freq float64) float64 {
	if t < 0 {
		return 0
	}

	envelope := math.Exp(-chimeDecay * t)

	if t < chimeAttack {
		envelope *= t / chimeAttack
	}

	phase := 2 * math.Pi * freq * t

	tone := math.Sin(phase) + 0.4*math.Exp(-2*chimeDecay*t)*math.Sin(2*phase) + 0.15*math.Exp(-4*chimeDecay*t)*math.Sin(3*phase)

	return envelope * tone / 1.55 // 1.55 = sum of harmonic amplitudes
}
