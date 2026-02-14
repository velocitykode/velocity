// Package converters provides utilities for converting between Go types and protobuf types.
package converters

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TimeToProto converts a time.Time pointer to a protobuf Timestamp.
// Returns nil if the input is nil.
func TimeToProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// TimeValueToProto converts a time.Time value to a protobuf Timestamp.
func TimeValueToProto(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

// ProtoToTime converts a protobuf Timestamp to a time.Time pointer.
// Returns nil if the input is nil.
func ProtoToTime(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// ProtoToTimeValue converts a protobuf Timestamp to a time.Time value.
// Returns zero time if the input is nil.
func ProtoToTimeValue(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// NowProto returns the current time as a protobuf Timestamp.
func NowProto() *timestamppb.Timestamp {
	return timestamppb.Now()
}

// DurationToProto converts a time.Duration to a protobuf Duration.
func DurationToProto(d time.Duration) *durationpb.Duration {
	return durationpb.New(d)
}

// ProtoToDuration converts a protobuf Duration to a time.Duration.
// Returns 0 if the input is nil.
func ProtoToDuration(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return d.AsDuration()
}

// TimeOrNil returns the time as a pointer, or nil if zero.
func TimeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// TimeOrZero returns the time value, or zero time if nil.
func TimeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
