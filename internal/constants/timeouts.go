package constants

import "time"

// Timeout constants used throughout the application
const (
	// ReadTimeout is the timeout for read operations
	ReadTimeout = 1 * time.Second

	// WriteTimeout is the timeout for write operations
	WriteTimeout = 10 * time.Second

	// HandlerTimeout is the timeout for handler operations
	HandlerTimeout = 5 * time.Second

	// SSHConnectionTimeout is the timeout for SSH connection attempts
	SSHConnectionTimeout = 30 * time.Second

	// ReconnectSleepDuration is how long to wait before reconnecting
	ReconnectSleepDuration = 2 * time.Second

	// StatsTimerDuration is the interval for client stats reporting
	StatsTimerDuration = 3 * time.Second

	// ServerStatsTimerDuration is the interval for server stats reporting
	ServerStatsTimerDuration = 10 * time.Second

	// DefaultMapReduceInterval is the default interval for MapReduce operations
	DefaultMapReduceInterval = 5 * time.Second

	// ProcessorSleepDuration is the sleep duration for processors
	ProcessorSleepDuration = 10 * time.Millisecond

	// ProcessorTimeoutDuration is the timeout for processor operations
	ProcessorTimeoutDuration = 100 * time.Millisecond

	// MapReduceSleepDuration is the sleep duration in MapReduce aggregation
	MapReduceSleepDuration = 100 * time.Millisecond

	// ContinuousJobsStartDelay is the delay before starting continuous jobs
	ContinuousJobsStartDelay = 2 * time.Second

	// SchedulerStartDelay is the delay before starting the scheduler
	SchedulerStartDelay = 2 * time.Second

	// RetryTimerDuration is the duration for retry operations
	RetryTimerDuration = 2 * time.Second

	// SSHDialTimeout is the timeout for SSH dial operations
	SSHDialTimeout = 2 * time.Second

	// KnownHostsCallbackTimeout is the timeout for known hosts callback
	KnownHostsCallbackTimeout = 2 * time.Second

	// ReadCommandRetryInterval is the interval between read command retries
	ReadCommandRetryInterval = 5 * time.Second

	// DayDuration represents 24 hours for date calculations
	DayDuration = 24 * time.Hour
)