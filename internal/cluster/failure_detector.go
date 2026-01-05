package cluster

import "time"

type FailureDetectorConfig struct {
	HeartbeatInterval time.Duration
	SuspectAfter      time.Duration
	DeadAfter         time.Duration
}

func DefaultFailureDetectorConfig() FailureDetectorConfig {
	return FailureDetectorConfig{
		HeartbeatInterval: 300 * time.Millisecond,
		SuspectAfter:      900 * time.Millisecond,
		DeadAfter:         2500 * time.Millisecond,
	}
}
