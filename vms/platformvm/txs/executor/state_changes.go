// Copyright (C) 2022-2023, Chain4Travel AG. All rights reserved.
//
// This file is a derived work, based on ava-labs code whose
// original notices appear below.
//
// It is distributed under the same license conditions as the
// original code from which it is derived.
//
// Much love to the original authors for their work.
// **********************************************************
// Copyright (C) 2019-2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package executor

import (
	"errors"
	"fmt"
	"time"

	"github.com/ava-labs/avalanchego/chains/atomic"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
	"github.com/ava-labs/avalanchego/vms/platformvm/state"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
)

var (
	errChildBlockAfterStakerChangeTime = errors.New("proposed timestamp later than next staker change time")
	errChildBlockBeyondSyncBound       = errors.New("proposed timestamp is too far in the future relative to local time")
)

// VerifyNewChainTime returns nil if the [newChainTime] is a valid chain time
// given the wall clock time ([now]) and when the next staking set change occurs
// ([nextStakerChangeTime]).
// Requires:
//   - [newChainTime] <= [nextStakerChangeTime]: so that no staking set changes
//     are skipped.
//   - [newChainTime] <= [now] + [SyncBound]: to ensure chain time approximates
//     "real" time.
func VerifyNewChainTime(
	newChainTime,
	nextStakerChangeTime,
	now time.Time,
) error {
	// Only allow timestamp to move as far forward as the time of the next
	// staker set change
	if newChainTime.After(nextStakerChangeTime) {
		return fmt.Errorf(
			"%w, proposed timestamp (%s), next staker change time (%s)",
			errChildBlockAfterStakerChangeTime,
			newChainTime,
			nextStakerChangeTime,
		)
	}

	// Only allow timestamp to reasonably far forward
	maxNewChainTime := now.Add(SyncBound)
	if newChainTime.After(maxNewChainTime) {
		return fmt.Errorf(
			"%w, proposed time (%s), local time (%s)",
			errChildBlockBeyondSyncBound,
			newChainTime,
			now,
		)
	}
	return nil
}

type StateChanges interface {
	Apply(onAccept state.Diff)
	AtomicChanges() (set.Set[ids.ID], map[ids.ID]*atomic.Requests)
	Len() int
}

type stateChanges struct {
	updatedSupplies           map[ids.ID]uint64
	currentValidatorsToAdd    []*state.Staker
	currentDelegatorsToAdd    []*state.Staker
	pendingValidatorsToRemove []*state.Staker
	pendingDelegatorsToRemove []*state.Staker
	currentValidatorsToRemove []*state.Staker

	caminoStateChanges
}

func (s *stateChanges) Apply(stateDiff state.Diff) {
	for subnetID, supply := range s.updatedSupplies {
		stateDiff.SetCurrentSupply(subnetID, supply)
	}

	for _, currentValidatorToAdd := range s.currentValidatorsToAdd {
		stateDiff.PutCurrentValidator(currentValidatorToAdd)
	}
	for _, pendingValidatorToRemove := range s.pendingValidatorsToRemove {
		stateDiff.DeletePendingValidator(pendingValidatorToRemove)
	}
	for _, currentDelegatorToAdd := range s.currentDelegatorsToAdd {
		stateDiff.PutCurrentDelegator(currentDelegatorToAdd)
	}
	for _, pendingDelegatorToRemove := range s.pendingDelegatorsToRemove {
		stateDiff.DeletePendingDelegator(pendingDelegatorToRemove)
	}
	for _, currentValidatorToRemove := range s.currentValidatorsToRemove {
		stateDiff.DeleteCurrentValidator(currentValidatorToRemove)
	}

	s.caminoStateChanges.Apply(stateDiff)
}

func (s *stateChanges) Len() int {
	return len(s.currentValidatorsToAdd) + len(s.currentDelegatorsToAdd) +
		len(s.pendingValidatorsToRemove) + len(s.pendingDelegatorsToRemove) +
		len(s.currentValidatorsToRemove) + s.caminoStateChanges.Len()
}

// AdvanceTimeTo does not modify [parentState].
// Instead it returns all the StateChanges caused by advancing the chain time to
// the [newChainTime].
func AdvanceTimeTo(
	backend *Backend,
	parentState state.Chain,
	newChainTime time.Time,
) (StateChanges, error) {
	pendingStakerIterator, err := parentState.GetPendingStakerIterator()
	if err != nil {
		return nil, err
	}
	defer pendingStakerIterator.Release()

	changes := &stateChanges{
		updatedSupplies: make(map[ids.ID]uint64),
	}

	// Add to the staker set any pending stakers whose start time is at or
	// before the new timestamp
	for pendingStakerIterator.Next() {
		stakerToRemove := pendingStakerIterator.Value()
		if stakerToRemove.StartTime.After(newChainTime) {
			break
		}
		if stakerToRemove.EndTime.Equal(stakerToRemove.StartTime) {
			continue
		}

		stakerToAdd := *stakerToRemove
		stakerToAdd.NextTime = stakerToRemove.EndTime
		stakerToAdd.Priority = txs.PendingToCurrentPriorities[stakerToRemove.Priority]

		if stakerToRemove.Priority == txs.SubnetPermissionedValidatorPendingPriority {
			// Invariant: [txTimestamp] <= [nextStakerChangeTime].
			// Invariant: minimum stake duration is > 0.
			//
			// Both of the above invariants ensure the staker we are adding here
			// should never be attempted to be removed in the following loop.

			changes.currentValidatorsToAdd = append(changes.currentValidatorsToAdd, &stakerToAdd)
			changes.pendingValidatorsToRemove = append(changes.pendingValidatorsToRemove, stakerToRemove)
			continue
		}

		supply, ok := changes.updatedSupplies[stakerToRemove.SubnetID]
		if !ok {
			supply, err = parentState.GetCurrentSupply(stakerToRemove.SubnetID)
			if err != nil {
				return nil, err
			}
		}

		rewards, err := GetRewardsCalculator(backend, parentState, stakerToRemove.SubnetID)
		if err != nil {
			return nil, err
		}

		potentialReward := rewards.Calculate(
			stakerToRemove.EndTime.Sub(stakerToRemove.StartTime),
			stakerToRemove.Weight,
			supply,
		)
		stakerToAdd.PotentialReward = potentialReward

		// Invariant: [rewards.Calculate] can never return a [potentialReward]
		//            such that [supply + potentialReward > maximumSupply].
		changes.updatedSupplies[stakerToRemove.SubnetID] = supply + potentialReward

		switch stakerToRemove.Priority {
		case txs.PrimaryNetworkValidatorPendingPriority, txs.SubnetPermissionlessValidatorPendingPriority:
			changes.currentValidatorsToAdd = append(changes.currentValidatorsToAdd, &stakerToAdd)
			changes.pendingValidatorsToRemove = append(changes.pendingValidatorsToRemove, stakerToRemove)

		case txs.PrimaryNetworkDelegatorApricotPendingPriority, txs.PrimaryNetworkDelegatorBanffPendingPriority, txs.SubnetPermissionlessDelegatorPendingPriority:
			changes.currentDelegatorsToAdd = append(changes.currentDelegatorsToAdd, &stakerToAdd)
			changes.pendingDelegatorsToRemove = append(changes.pendingDelegatorsToRemove, stakerToRemove)

		default:
			return nil, fmt.Errorf("expected staker priority got %d", stakerToRemove.Priority)
		}
	}

	currentStakerIterator, err := parentState.GetCurrentStakerIterator()
	if err != nil {
		return nil, err
	}
	defer currentStakerIterator.Release()

	for currentStakerIterator.Next() {
		stakerToRemove := currentStakerIterator.Value()
		if stakerToRemove.EndTime.After(newChainTime) {
			break
		}

		// Invariant: Permissioned stakers are encountered first for a given
		//            timestamp because their priority is the smallest.
		if stakerToRemove.Priority != txs.SubnetPermissionedValidatorCurrentPriority {
			// Permissionless stakers are removed by the RewardValidatorTx, not
			// an AdvanceTimeTx.
			break
		}

		changes.currentValidatorsToRemove = append(changes.currentValidatorsToRemove, stakerToRemove)
	}

	if err := caminoAdvanceTimeTo(backend, parentState, newChainTime, changes); err != nil {
		return nil, err
	}

	return changes, nil
}

func GetRewardsCalculator(
	backend *Backend,
	parentState state.Chain,
	subnetID ids.ID,
) (reward.Calculator, error) {
	if subnetID == constants.PrimaryNetworkID {
		return backend.Rewards, nil
	}

	transformSubnetIntf, err := parentState.GetSubnetTransformation(subnetID)
	if err != nil {
		return nil, err
	}
	transformSubnet, ok := transformSubnetIntf.Unsigned.(*txs.TransformSubnetTx)
	if !ok {
		return nil, errIsNotTransformSubnetTx
	}

	return reward.NewCalculator(reward.Config{
		MaxConsumptionRate: transformSubnet.MaxConsumptionRate,
		MinConsumptionRate: transformSubnet.MinConsumptionRate,
		MintingPeriod:      backend.Config.RewardConfig.MintingPeriod,
		SupplyCap:          transformSubnet.MaximumSupply,
	}), nil
}
