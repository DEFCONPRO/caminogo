// Copyright (C) 2022-2023, Chain4Travel AG. All rights reserved.
// See the file LICENSE for licensing terms.

package p

import (
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
)

// backend

func (b *backendVisitor) AddressStateTx(tx *txs.AddressStateTx) error {
	return b.baseTx(&tx.BaseTx)
}

func (b *backendVisitor) DepositTx(tx *txs.DepositTx) error {
	return b.baseTx(&tx.BaseTx)
}

func (b *backendVisitor) UnlockDepositTx(tx *txs.UnlockDepositTx) error {
	return b.baseTx(&tx.BaseTx)
}

func (b *backendVisitor) ClaimRewardTx(tx *txs.ClaimRewardTx) error {
	return b.baseTx(&tx.BaseTx)
}

func (b *backendVisitor) RegisterNodeTx(tx *txs.RegisterNodeTx) error {
	return b.baseTx(&tx.BaseTx)
}

func (*backendVisitor) RewardsImportTx(*txs.RewardsImportTx) error {
	return errUnsupportedTxType
}

func (s *signerVisitor) AddressStateTx(tx *txs.AddressStateTx) error {
	txSigners, err := s.getSigners(constants.PlatformChainID, tx.Ins)
	if err != nil {
		return err
	}
	return sign(s.tx, txSigners)
}

func (s *signerVisitor) DepositTx(tx *txs.DepositTx) error {
	txSigners, err := s.getSigners(constants.PlatformChainID, tx.Ins)
	if err != nil {
		return err
	}
	return sign(s.tx, txSigners)
}

func (s *signerVisitor) UnlockDepositTx(tx *txs.UnlockDepositTx) error {
	txSigners, err := s.getSigners(constants.PlatformChainID, tx.Ins)
	if err != nil {
		return err
	}
	return sign(s.tx, txSigners)
}

func (s *signerVisitor) ClaimRewardTx(tx *txs.ClaimRewardTx) error {
	txSigners, err := s.getSigners(constants.PlatformChainID, tx.Ins)
	if err != nil {
		return err
	}
	return sign(s.tx, txSigners)
}

func (s *signerVisitor) RegisterNodeTx(tx *txs.RegisterNodeTx) error {
	txSigners, err := s.getSigners(constants.PlatformChainID, tx.Ins)
	if err != nil {
		return err
	}
	return sign(s.tx, txSigners)
}

func (*signerVisitor) RewardsImportTx(*txs.RewardsImportTx) error {
	return errUnsupportedTxType
}
