// Copyright (C) 2022, Chain4Travel AG. All rights reserved.
// See the file LICENSE for licensing terms.

package platformvm

import (
	"context"
	"fmt"
	"time"

	"github.com/ava-labs/avalanchego/chains"
	"github.com/ava-labs/avalanchego/chains/atomic"
	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/database/manager"
	"github.com/ava-labs/avalanchego/database/prefixdb"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/engine/common"
	"github.com/ava-labs/avalanchego/snow/uptime"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto"
	"github.com/ava-labs/avalanchego/utils/formatting"
	"github.com/ava-labs/avalanchego/utils/formatting/address"
	"github.com/ava-labs/avalanchego/utils/json"
	"github.com/ava-labs/avalanchego/utils/nodeid"
	"github.com/ava-labs/avalanchego/utils/timer/mockable"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/version"
	"github.com/ava-labs/avalanchego/vms/platformvm/api"
	"github.com/ava-labs/avalanchego/vms/platformvm/config"
	"github.com/ava-labs/avalanchego/vms/platformvm/genesis"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
)

const (
	defaultCaminoValidatorWeight = 2 * units.KiloAvax
)

var (
	localStakingPath          = "../../staking/local/"
	_, caminoPreFundedNodeIDs = nodeid.LoadLocalCaminoNodeKeysAndIDs(localStakingPath)
)

func newCaminoVM(genesisConfig genesis.Camino, genesisUTXOs []api.UTXO) (*VM, database.Database, *mutableSharedMemory) {
	vm := &VM{Factory: Factory{defaultCaminoConfig(true)}}

	baseDBManager := manager.NewMemDB(version.Semantic1_0_0)
	chainDBManager := baseDBManager.NewPrefixDBManager([]byte{0})
	atomicDB := prefixdb.New([]byte{1}, baseDBManager.Current().Database)

	vm.clock.Set(banffForkTime.Add(time.Second))
	msgChan := make(chan common.Message, 1)
	ctx := defaultContext()

	m := atomic.NewMemory(atomicDB)
	msm := &mutableSharedMemory{
		SharedMemory: m.NewSharedMemory(ctx.ChainID),
	}
	ctx.SharedMemory = msm

	ctx.Lock.Lock()
	defer ctx.Lock.Unlock()
	_, genesisBytes := newCaminoGenesisWithUTXOs(genesisConfig, genesisUTXOs)
	appSender := &common.SenderTest{}
	appSender.CantSendAppGossip = true
	appSender.SendAppGossipF = func(context.Context, []byte) error { return nil }

	if err := vm.Initialize(ctx, chainDBManager, genesisBytes, nil, nil, msgChan, nil, appSender); err != nil {
		panic(err)
	}
	if err := vm.SetState(snow.NormalOp); err != nil {
		panic(err)
	}

	// Create a subnet and store it in testSubnet1
	// Note: following Banff activation, block acceptance will move
	// chain time ahead
	var err error
	testSubnet1, err = vm.txBuilder.NewCreateSubnetTx(
		2, // threshold; 2 sigs from keys[0], keys[1], keys[2] needed to add validator to this subnet
		// control keys are keys[0], keys[1], keys[2]
		[]ids.ShortID{keys[0].PublicKey().Address(), keys[1].PublicKey().Address(), keys[2].PublicKey().Address()},
		[]*crypto.PrivateKeySECP256K1R{keys[0]}, // pays tx fee
		keys[0].PublicKey().Address(),           // change addr
	)
	if err != nil {
		panic(err)
	} else if err := vm.Builder.AddUnverifiedTx(testSubnet1); err != nil {
		panic(err)
	} else if blk, err := vm.Builder.BuildBlock(); err != nil {
		panic(err)
	} else if err := blk.Verify(); err != nil {
		panic(err)
	} else if err := blk.Accept(); err != nil {
		panic(err)
	} else if err := vm.SetPreference(vm.manager.LastAccepted()); err != nil {
		panic(err)
	}

	return vm, baseDBManager.Current().Database, msm
}

func defaultCaminoConfig(postBanff bool) config.Config {
	banffTime := mockable.MaxTime
	if postBanff {
		banffTime = defaultValidateEndTime.Add(-2 * time.Second)
	}
	return config.Config{
		Chains:                 chains.MockManager{},
		UptimeLockedCalculator: uptime.NewLockedCalculator(),
		Validators:             validators.NewManager(),
		TxFee:                  defaultTxFee,
		CreateSubnetTxFee:      100 * defaultTxFee,
		CreateBlockchainTxFee:  100 * defaultTxFee,
		MinValidatorStake:      defaultCaminoValidatorWeight,
		MaxValidatorStake:      defaultCaminoValidatorWeight,
		MinDelegatorStake:      1 * units.MilliAvax,
		MinStakeDuration:       defaultMinStakingDuration,
		MaxStakeDuration:       defaultMaxStakingDuration,
		RewardConfig: reward.Config{
			MaxConsumptionRate: .12 * reward.PercentDenominator,
			MinConsumptionRate: .10 * reward.PercentDenominator,
			MintingPeriod:      365 * 24 * time.Hour,
			SupplyCap:          720 * units.MegaAvax,
		},
		ApricotPhase3Time: defaultValidateEndTime,
		ApricotPhase5Time: defaultValidateEndTime,
		BanffTime:         banffTime,
		CaminoConfig: config.CaminoConfig{
			DaoProposalBondAmount: 100 * units.Avax,
		},
	}
}

// Returns:
// 1) The genesis state
// 2) The byte representation of the default genesis for tests
func newCaminoGenesisWithUTXOs(caminoGenesisConfig genesis.Camino, genesisUTXOs []api.UTXO) (*api.BuildGenesisArgs, []byte) {
	hrp := constants.NetworkIDToHRP[testNetworkID]
	genesisValidators := make([]api.PermissionlessValidator, len(keys))
	for i, key := range keys {
		addr, err := address.FormatBech32(hrp, key.PublicKey().Address().Bytes())
		if err != nil {
			panic(err)
		}
		genesisValidators[i] = api.PermissionlessValidator{
			Staker: api.Staker{
				StartTime: json.Uint64(defaultValidateStartTime.Unix()),
				EndTime:   json.Uint64(defaultValidateEndTime.Unix()),
				NodeID:    caminoPreFundedNodeIDs[i],
			},
			RewardOwner: &api.Owner{
				Threshold: 1,
				Addresses: []string{addr},
			},
			Staked: []api.UTXO{{
				Amount:  json.Uint64(defaultWeight),
				Address: addr,
			}},
		}
	}

	buildGenesisArgs := api.BuildGenesisArgs{
		Encoding:      formatting.Hex,
		NetworkID:     json.Uint32(testNetworkID),
		AvaxAssetID:   avaxAssetID,
		UTXOs:         genesisUTXOs,
		Validators:    genesisValidators,
		Chains:        nil,
		Time:          json.Uint64(defaultGenesisTime.Unix()),
		InitialSupply: json.Uint64(360 * units.MegaAvax),
		Camino:        caminoGenesisConfig,
	}

	buildGenesisResponse := api.BuildGenesisReply{}
	platformvmSS := api.StaticService{}
	if err := platformvmSS.BuildGenesis(nil, &buildGenesisArgs, &buildGenesisResponse); err != nil {
		panic(fmt.Errorf("problem while building platform chain's genesis state: %w", err))
	}

	genesisBytes, err := formatting.Decode(buildGenesisResponse.Encoding, buildGenesisResponse.Bytes)
	if err != nil {
		panic(err)
	}

	return &buildGenesisArgs, genesisBytes
}
