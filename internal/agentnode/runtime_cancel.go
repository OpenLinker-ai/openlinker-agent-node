package agentnode

import (
	"context"
	"errors"
	"fmt"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func (node *Node) commandLoop() {
	attempt := 0
	for {
		if node.runtimeCtx.Err() != nil {
			return
		}
		response, err := node.RuntimeClient.PollRuntimeV2Commands(
			node.runtimeCtx,
			node.store.Identity().RuntimeSessionID,
			durationSeconds(node.CommandWait),
		)
		if err != nil {
			if runtimeErrorIsPermanent(err) || durableRuntimeErrorIsFatal(err) {
				node.reportFatal(scrubRuntimeError(err))
				return
			}
			if node.waitRetry(node.runtimeCtx, attempt) != nil {
				return
			}
			attempt++
			continue
		}
		attempt = 0
		if response == nil {
			node.reportFatal(fmt.Errorf("%w: command poll response", ErrRuntimeProtocolMismatch))
			return
		}
		for _, command := range response.Commands {
			decoded, err := command.Decode()
			if err != nil {
				node.reportFatal(err)
				return
			}
			node.handleDecodedCommand(decoded)
		}
	}
}

func (node *Node) handleDecodedCommand(command openlinker.RuntimeV2DecodedPendingCommand) {
	switch command.Type {
	case openlinker.RuntimeV2RunCancel:
		if command.Cancel == nil || !node.beginCancellation(command.Cancel.CancellationID) {
			return
		}
		node.loops.Add(1)
		go func() {
			defer node.loops.Done()
			node.handleCancelCommand(*command.Cancel)
		}()
	case openlinker.RuntimeV2Drain:
		node.setDraining(true)
	case openlinker.RuntimeV2LeaseRevoked:
		if command.Revoke != nil {
			node.handleLeaseRevoke(*command.Revoke)
		}
	}
}

func (node *Node) beginCancellation(cancellationID string) bool {
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	if _, exists := node.cancellations[cancellationID]; exists {
		return false
	}
	node.cancellations[cancellationID] = struct{}{}
	return true
}

func (node *Node) handleCancelCommand(command openlinker.RuntimeV2RunCancelPayload) {
	record, err := node.assignmentByAttempt(command.AttemptIdentity.AttemptID)
	if err != nil || sdkAttemptIdentity(record.Identity) != command.AttemptIdentity {
		_ = node.ackCancelUntil(command, openlinker.RuntimeV2CancelFailed, "ATTEMPT_IDENTITY_MISMATCH")
		return
	}
	if err := node.ackCancelOnce(command, openlinker.RuntimeV2CancelStopping, ""); err != nil {
		node.logf("runtime cancel stopping ACK was not confirmed: %v", scrubRuntimeError(err))
	}
	active := node.activeAttempt(record.Identity.AttemptID)
	if active != nil {
		active.canceled.Store(true)
		active.cancel()
		waitUntil := command.DeadlineAt
		if waitUntil.IsZero() {
			waitUntil = time.Now().Add(DefaultShutdownTimeout)
		}
		timer := time.NewTimer(time.Until(waitUntil))
		select {
		case <-active.done:
			timer.Stop()
		case <-timer.C:
			_ = node.ackCancelUntil(command, openlinker.RuntimeV2CancelFailed, "CANCEL_DEADLINE_EXCEEDED")
			return
		case <-node.runtimeCtx.Done():
			timer.Stop()
			return
		}
	}
	if err := node.ackCancelUntil(command, openlinker.RuntimeV2CancelStopped, ""); err != nil {
		node.logf("runtime cancel stopped ACK will retry: %v", scrubRuntimeError(err))
		return
	}
	node.stateMu.Lock()
	delete(node.spoolAllowed, record.Identity.AttemptID)
	node.stateMu.Unlock()
	current, err := node.store.Assignment(record.Identity.AssignmentMessageID)
	if err != nil {
		if !errors.Is(err, ErrAssignmentNotFound) && node.runtimeCtx.Err() == nil {
			node.reportFatal(err)
		}
		return
	}
	if current.State != AssignmentStateResultACKed && current.State != AssignmentStateRejected && current.State != AssignmentStateRevoked {
		if _, err := node.store.AdvanceAssignment(record.Identity.AssignmentMessageID, AssignmentStateRevoked); err != nil {
			node.reportFatal(err)
		}
	}
}

func (node *Node) ackCancelOnce(command openlinker.RuntimeV2RunCancelPayload, state openlinker.RuntimeV2CancelState, errorCode string) error {
	ctx, cancel := context.WithTimeout(node.runtimeCtx, 2*time.Second)
	defer cancel()
	_, err := node.RuntimeClient.AckRuntimeV2Cancel(ctx, openlinker.RuntimeV2RunCancelAckPayload{
		CancellationID:  command.CancellationID,
		AttemptIdentity: command.AttemptIdentity,
		CancelState:     state,
		ErrorCode:       errorCode,
	})
	return err
}

func (node *Node) ackCancelUntil(command openlinker.RuntimeV2RunCancelPayload, state openlinker.RuntimeV2CancelState, errorCode string) error {
	deadline := command.DeadlineAt
	for attempt := 0; ; attempt++ {
		ctx := node.runtimeCtx
		var cancel context.CancelFunc = func() {}
		if !deadline.IsZero() {
			ctx, cancel = context.WithDeadline(node.runtimeCtx, deadline)
		}
		_, err := node.RuntimeClient.AckRuntimeV2Cancel(ctx, openlinker.RuntimeV2RunCancelAckPayload{
			CancellationID:  command.CancellationID,
			AttemptIdentity: command.AttemptIdentity,
			CancelState:     state,
			ErrorCode:       errorCode,
		})
		cancel()
		if err == nil {
			return nil
		}
		if runtimeErrorIsPermanent(err) || (!deadline.IsZero() && !time.Now().Before(deadline)) {
			return err
		}
		if node.waitRetry(node.runtimeCtx, attempt) != nil {
			return node.runtimeCtx.Err()
		}
	}
}

func (node *Node) handleLeaseRevoke(command openlinker.RuntimeV2RunLeaseRevokedPayload) {
	record, err := node.assignmentByAttempt(command.AttemptIdentity.AttemptID)
	if err != nil || sdkAttemptIdentity(record.Identity) != command.AttemptIdentity {
		return
	}
	node.revokeLocalAttempt(record)
}

func (node *Node) renewAttemptLease(attempt *activeRuntimeAttempt) {
	defer close(attempt.renewDone)
	interval := node.leaseRenewInterval()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	retry := 0
	for {
		select {
		case <-node.runtimeCtx.Done():
			return
		case <-attempt.renewStop:
			return
		case <-timer.C:
		}
		if expiry := attempt.leaseExpiry(); !expiry.IsZero() && !time.Now().Before(expiry) {
			attempt.canceled.Store(true)
			attempt.cancel()
			if record, err := node.store.Assignment(attempt.identity.AssignmentMessageID); err == nil {
				node.revokeLocalAttempt(record)
			}
			return
		}
		record, err := node.store.Assignment(attempt.identity.AssignmentMessageID)
		if err != nil {
			if node.runtimeCtx.Err() != nil || (attempt.finished.Load() && errors.Is(err, ErrAssignmentNotFound)) {
				return
			}
			node.reportFatal(err)
			return
		}
		capacity, inflight := node.capacitySnapshot()
		renewCtx := node.runtimeCtx
		var cancelRenew context.CancelFunc
		if expiry := attempt.leaseExpiry(); !expiry.IsZero() {
			renewCtx, cancelRenew = context.WithDeadline(node.runtimeCtx, expiry)
		} else {
			renewCtx, cancelRenew = context.WithTimeout(node.runtimeCtx, 10*time.Second)
		}
		renewed, err := node.RuntimeClient.RenewRuntimeV2Lease(renewCtx, openlinker.RuntimeV2LeaseRenewPayload{
			AttemptIdentity:    sdkAttemptIdentity(attempt.identity),
			LastClientEventSeq: record.LastClientEventSeq,
			Capacity:           capacity,
			Inflight:           inflight,
		})
		cancelRenew()
		if err != nil {
			code := runtimeErrorCode(err)
			if code == "STALE_LEASE" || code == "LEASE_EXPIRED" || code == "RUN_CANCEL_REQUESTED" {
				attempt.canceled.Store(true)
				attempt.cancel()
				node.revokeLocalAttempt(record)
				return
			}
			if runtimeErrorIsPermanent(err) {
				node.reportFatal(scrubRuntimeError(err))
				attempt.canceled.Store(true)
				attempt.cancel()
				return
			}
			if expiry := attempt.leaseExpiry(); !expiry.IsZero() && !time.Now().Before(expiry) {
				attempt.canceled.Store(true)
				attempt.cancel()
				node.revokeLocalAttempt(record)
				return
			}
			delay := node.retryDelay(retry)
			if node.jitter != nil {
				delay = node.jitter(delay)
			} else {
				delay = jitterDuration(delay)
			}
			retry++
			if expiry := attempt.leaseExpiry(); !expiry.IsZero() && delay > time.Until(expiry) {
				delay = max(time.Until(expiry), time.Millisecond)
			}
			timer.Reset(delay)
			continue
		}
		retry = 0
		if renewed == nil || renewed.AttemptIdentity != sdkAttemptIdentity(attempt.identity) || !renewed.LeaseExpiresAt.After(time.Now()) {
			node.reportFatal(fmt.Errorf("%w: lease renewal", ErrRuntimeProtocolMismatch))
			attempt.canceled.Store(true)
			attempt.cancel()
			return
		}
		attempt.setLeaseExpiry(renewed.LeaseExpiresAt)
		if renewed.PendingCommand != nil {
			decoded, decodeErr := renewed.PendingCommand.Decode()
			if decodeErr != nil {
				node.reportFatal(decodeErr)
				return
			}
			node.handleDecodedCommand(decoded)
		}
		timer.Reset(interval)
	}
}

func (node *Node) leaseRenewInterval() time.Duration {
	node.stateMu.RLock()
	ready := node.ready
	node.stateMu.RUnlock()
	if ready != nil && ready.LeaseTTLSeconds > 0 {
		interval := time.Duration(ready.LeaseTTLSeconds) * time.Second / 3
		if interval < 250*time.Millisecond {
			interval = 250 * time.Millisecond
		}
		return interval
	}
	return DefaultHeartbeatInterval
}

func (node *Node) assignmentByAttempt(attemptID string) (AssignmentJournalRecord, error) {
	records, err := node.store.Assignments()
	if err != nil {
		return AssignmentJournalRecord{}, err
	}
	for _, record := range records {
		if record.Identity.AttemptID == attemptID {
			return record, nil
		}
	}
	return AssignmentJournalRecord{}, ErrAssignmentNotFound
}
