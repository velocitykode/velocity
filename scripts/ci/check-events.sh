#!/usr/bin/env bash
# check-events.sh - reports framework event code that breaks one of three
# rules: an event field whose type does not survive the queue codec (an
# interface, a type parameter, an error without the event's own codec), so
# an async listener would see a different value than a sync one; an event
# the framework builds without the contract.EventMeta envelope; and an
# event emitter that records failures it originates into Failures the app
# never shares with it, so they miss App.FailedEventCount.
#
# Delegates to the go/types walker in scripts/ci/check-events/, whose
# package comment documents the rules and their scope. There is no
# suppression marker.
#
# Prints "file:line: rule: message" per offender, then on stderr how to fix
# each rule reported, and exits non-zero when there is any. Prints nothing
# on success.

set -euo pipefail

cd "$(dirname "$0")/../.."

exec go run ./scripts/ci/check-events ./...
