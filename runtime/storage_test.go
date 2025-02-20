/*
 * Cadence - The resource-oriented smart contract programming language
 *
 * Copyright 2019-2020 Dapper Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package runtime

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/onflow/atree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onflow/cadence"
	"github.com/onflow/cadence/encoding/json"
	"github.com/onflow/cadence/runtime/common"
	"github.com/onflow/cadence/runtime/interpreter"
	"github.com/onflow/cadence/runtime/tests/utils"
)

func withWritesToStorage(
	tb testing.TB,
	arrayElementCount int,
	storageItemCount int,
	onWrite func(owner, key, value []byte),
	handler func(*Storage, *interpreter.Interpreter),
) {

	storage := NewStorage(
		newTestLedger(nil, onWrite),
		func(f func(), _ func(metrics Metrics, duration time.Duration)) {
			f()
		},
	)

	inter := newTestInterpreter(tb)

	array := interpreter.NewArrayValue(
		inter,
		interpreter.VariableSizedStaticType{
			Type: interpreter.PrimitiveStaticTypeInt,
		},
		common.Address{},
	)

	for i := 0; i < arrayElementCount; i++ {
		array.Append(
			inter,
			interpreter.ReturnEmptyLocationRange,
			interpreter.NewIntValueFromInt64(int64(i)),
		)
	}

	address := common.BytesToAddress([]byte{0x1})

	for i := 0; i < storageItemCount; i++ {
		storable, err := array.Storable(
			inter.Storage,
			atree.Address(address),
			math.MaxUint64,
		)
		require.NoError(tb, err)

		storage.writes[interpreter.StorageKey{
			Address: address,
			Key:     strconv.Itoa(i),
		}] = storable
	}

	handler(storage, inter)
}

func TestRuntimeStorageWriteCached(t *testing.T) {

	t.Parallel()

	var writes []testWrite

	onWrite := func(owner, key, value []byte) {
		writes = append(writes, testWrite{
			owner: owner,
			key:   key,
			value: value,
		})
	}

	const arrayElementCount = 100
	const storageItemCount = 100
	withWritesToStorage(
		t,
		arrayElementCount,
		storageItemCount,
		onWrite,
		func(storage *Storage, inter *interpreter.Interpreter) {
			const commitContractUpdates = true
			err := storage.Commit(inter, commitContractUpdates)
			require.NoError(t, err)

			require.Len(t, writes, storageItemCount)
		},
	)
}

func TestRuntimeStorageWriteCachedIsDeterministic(t *testing.T) {

	t.Parallel()

	var writes []testWrite

	onWrite := func(owner, key, value []byte) {
		writes = append(writes, testWrite{
			owner: owner,
			key:   key,
			value: value,
		})
	}

	const arrayElementCount = 100
	const storageItemCount = 100
	withWritesToStorage(
		t,
		arrayElementCount,
		storageItemCount,
		onWrite,
		func(storage *Storage, inter *interpreter.Interpreter) {
			const commitContractUpdates = true
			err := storage.Commit(inter, commitContractUpdates)
			require.NoError(t, err)

			previousWrites := make([]testWrite, len(writes))
			copy(previousWrites, writes)

			// verify for 10 times and check the writes are always deterministic
			for i := 0; i < 10; i++ {
				// test that writing again should produce the same result
				writes = nil
				err := storage.Commit(inter, commitContractUpdates)
				require.NoError(t, err)

				for i, previousWrite := range previousWrites {
					// compare the new write with the old write
					require.Equal(t, previousWrite, writes[i])
				}

				// no additional items
				require.Len(t, writes, len(previousWrites))
			}
		},
	)
}

func BenchmarkRuntimeStorageWriteCached(b *testing.B) {
	var writes []testWrite

	onWrite := func(owner, key, value []byte) {
		writes = append(writes, testWrite{
			owner: owner,
			key:   key,
			value: value,
		})
	}

	const arrayElementCount = 100
	const storageItemCount = 100
	withWritesToStorage(
		b,
		arrayElementCount,
		storageItemCount,
		onWrite,
		func(storage *Storage, inter *interpreter.Interpreter) {
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				writes = nil
				const commitContractUpdates = true
				err := storage.Commit(inter, commitContractUpdates)
				require.NoError(b, err)

				require.Len(b, writes, storageItemCount)
			}
		},
	)
}

func TestRuntimeStorageWrite(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	address := common.BytesToAddress([]byte{0x1})

	tx := []byte(`
      transaction {
          prepare(signer: AuthAccount) {
              signer.save(1, to: /storage/one)
          }
       }
    `)

	var writes []testWrite

	onWrite := func(owner, key, value []byte) {
		writes = append(writes, testWrite{
			owner,
			key,
			value,
		})
	}

	runtimeInterface := &testRuntimeInterface{
		storage: newTestLedger(nil, onWrite),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{address}, nil
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	err := runtime.ExecuteTransaction(
		Script{
			Source: tx,
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	assert.Equal(t,
		[]testWrite{
			{
				[]byte{0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x1},
				[]byte("storage\x1fone"),
				[]byte{
					// CBOR
					// - tag
					0xd8, 0x98,
					// - positive bignum
					0xc2,
					// - byte string, length 1
					0x41,
					0x1,
				},
			},
		},
		writes,
	)
}

func TestRuntimeAccountStorage(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	script := []byte(`
      transaction {
        prepare(signer: AuthAccount) {
           let before = signer.storageUsed
           signer.save(42, to: /storage/answer)
           let after = signer.storageUsed
           log(after != before)
        }
      }
    `)

	var loggedMessages []string

	storage := newTestLedger(nil, nil)

	runtimeInterface := &testRuntimeInterface{
		storage: storage,
		getSigningAccounts: func() ([]Address, error) {
			return []Address{{42}}, nil
		},
		getStorageUsed: func(_ Address) (uint64, error) {
			var amount uint64 = 0

			for _, data := range storage.storedValues {
				amount += uint64(len(data))
			}

			return amount, nil
		},
		log: func(message string) {
			loggedMessages = append(loggedMessages, message)
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	err := runtime.ExecuteTransaction(
		Script{
			Source: script,
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	require.Equal(t,
		[]string{"true"},
		loggedMessages,
	)
}

func TestRuntimePublicCapabilityBorrowTypeConfusion(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	addressString, err := hex.DecodeString("aad3e26e406987c2")
	require.NoError(t, err)

	signingAddress := common.BytesToAddress(addressString)

	deployFTContractTx := utils.DeploymentTransaction("FungibleToken", []byte(realFungibleTokenContractInterface))

	const ducContract = `
      import FungibleToken from 0xaad3e26e406987c2

      pub contract DapperUtilityCoin: FungibleToken {

    // Total supply of DapperUtilityCoins in existence
    pub var totalSupply: UFix64

    // Event that is emitted when the contract is created
    pub event TokensInitialized(initialSupply: UFix64)

    // Event that is emitted when tokens are withdrawn from a Vault
    pub event TokensWithdrawn(amount: UFix64, from: Address?)

    // Event that is emitted when tokens are deposited to a Vault
    pub event TokensDeposited(amount: UFix64, to: Address?)

    // Event that is emitted when new tokens are minted
    pub event TokensMinted(amount: UFix64)

    // Event that is emitted when tokens are destroyed
    pub event TokensBurned(amount: UFix64)

    // Event that is emitted when a new minter resource is created
    pub event MinterCreated(allowedAmount: UFix64)

    // Event that is emitted when a new burner resource is created
    pub event BurnerCreated()

    // Vault
    //
    // Each user stores an instance of only the Vault in their storage
    // The functions in the Vault and governed by the pre and post conditions
    // in FungibleToken when they are called.
    // The checks happen at runtime whenever a function is called.
    //
    // Resources can only be created in the context of the contract that they
    // are defined in, so there is no way for a malicious user to create Vaults
    // out of thin air. A special Minter resource needs to be defined to mint
    // new tokens.
    //
    pub resource Vault: FungibleToken.Provider, FungibleToken.Receiver, FungibleToken.Balance {

        // holds the balance of a users tokens
        pub var balance: UFix64

        // initialize the balance at resource creation time
        init(balance: UFix64) {
            self.balance = balance
        }

        // withdraw
        //
        // Function that takes an integer amount as an argument
        // and withdraws that amount from the Vault.
        // It creates a new temporary Vault that is used to hold
        // the money that is being transferred. It returns the newly
        // created Vault to the context that called so it can be deposited
        // elsewhere.
        //
        pub fun withdraw(amount: UFix64): @FungibleToken.Vault {
            self.balance = self.balance - amount
            emit TokensWithdrawn(amount: amount, from: self.owner?.address)
            return <-create Vault(balance: amount)
        }

        // deposit
        //
        // Function that takes a Vault object as an argument and adds
        // its balance to the balance of the owners Vault.
        // It is allowed to destroy the sent Vault because the Vault
        // was a temporary holder of the tokens. The Vault's balance has
        // been consumed and therefore can be destroyed.
        pub fun deposit(from: @FungibleToken.Vault) {
            let vault <- from as! @DapperUtilityCoin.Vault
            self.balance = self.balance + vault.balance
            emit TokensDeposited(amount: vault.balance, to: self.owner?.address)
            vault.balance = 0.0
            destroy vault
        }

        destroy() {
            DapperUtilityCoin.totalSupply = DapperUtilityCoin.totalSupply - self.balance
        }
    }

    // createEmptyVault
    //
    // Function that creates a new Vault with a balance of zero
    // and returns it to the calling context. A user must call this function
    // and store the returned Vault in their storage in order to allow their
    // account to be able to receive deposits of this token type.
    //
    pub fun createEmptyVault(): @FungibleToken.Vault {
        return <-create Vault(balance: 0.0)
    }

    pub resource Administrator {
        // createNewMinter
        //
        // Function that creates and returns a new minter resource
        //
        pub fun createNewMinter(allowedAmount: UFix64): @Minter {
            emit MinterCreated(allowedAmount: allowedAmount)
            return <-create Minter(allowedAmount: allowedAmount)
        }

        // createNewBurner
        //
        // Function that creates and returns a new burner resource
        //
        pub fun createNewBurner(): @Burner {
            emit BurnerCreated()
            return <-create Burner()
        }
    }

    // Minter
    //
    // Resource object that token admin accounts can hold to mint new tokens.
    //
    pub resource Minter {

        // the amount of tokens that the minter is allowed to mint
        pub var allowedAmount: UFix64

        // mintTokens
        //
        // Function that mints new tokens, adds them to the total supply,
        // and returns them to the calling context.
        //
        pub fun mintTokens(amount: UFix64): @DapperUtilityCoin.Vault {
            pre {
                amount > UFix64(0): "Amount minted must be greater than zero"
                amount <= self.allowedAmount: "Amount minted must be less than the allowed amount"
            }
            DapperUtilityCoin.totalSupply = DapperUtilityCoin.totalSupply + amount
            self.allowedAmount = self.allowedAmount - amount
            emit TokensMinted(amount: amount)
            return <-create Vault(balance: amount)
        }

        init(allowedAmount: UFix64) {
            self.allowedAmount = allowedAmount
        }
    }

    // Burner
    //
    // Resource object that token admin accounts can hold to burn tokens.
    //
    pub resource Burner {

        // burnTokens
        //
        // Function that destroys a Vault instance, effectively burning the tokens.
        //
        // Note: the burned tokens are automatically subtracted from the
        // total supply in the Vault destructor.
        //
        pub fun burnTokens(from: @FungibleToken.Vault) {
            let vault <- from as! @DapperUtilityCoin.Vault
            let amount = vault.balance
            destroy vault
            emit TokensBurned(amount: amount)
        }
    }

    init() {
        // we're using a high value as the balance here to make it look like we've got a ton of money,
        // just in case some contract manually checks that our balance is sufficient to pay for stuff
        self.totalSupply = 999999999.0

        let admin <- create Administrator()
        let minter <- admin.createNewMinter(allowedAmount: self.totalSupply)
        self.account.save(<-admin, to: /storage/dapperUtilityCoinAdmin)

        // mint tokens
        let tokenVault <- minter.mintTokens(amount: self.totalSupply)
        self.account.save(<-tokenVault, to: /storage/dapperUtilityCoinVault)
        destroy minter

        // Create a public capability to the stored Vault that only exposes
        // the balance field through the Balance interface
        self.account.link<&DapperUtilityCoin.Vault{FungibleToken.Balance}>(
            /public/dapperUtilityCoinBalance,
            target: /storage/dapperUtilityCoinVault
        )

        // Create a public capability to the stored Vault that only exposes
        // the deposit method through the Receiver interface
        self.account.link<&{FungibleToken.Receiver}>(
            /public/dapperUtilityCoinReceiver,
            target: /storage/dapperUtilityCoinVault
        )

        // Emit an event that shows that the contract was initialized
        emit TokensInitialized(initialSupply: self.totalSupply)
    }
}

    `

	deployDucContractTx := utils.DeploymentTransaction("DapperUtilityCoin", []byte(ducContract))

	const testContract = `
      access(all) contract TestContract{
        pub struct fake{
          pub(set) var balance: UFix64

          init(){
            self.balance = 0.0
          }
        }
        pub resource resourceConverter{
          pub fun convert(b: fake): AnyStruct {
            b.balance = 100.0
            return b
          }
        }
        pub resource resourceConverter2{
          pub fun convert(b: @AnyResource): AnyStruct {
            destroy b
            return ""
          }
        }
        access(all) fun createConverter():  @resourceConverter{
            return <- create resourceConverter();
        }
      }
    `

	deployTestContractTx := utils.DeploymentTransaction("TestContract", []byte(testContract))

	accountCodes := map[common.LocationID][]byte{}
	var events []cadence.Event
	var loggedMessages []string

	runtimeInterface := &testRuntimeInterface{
		storage: newTestLedger(nil, nil),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signingAddress}, nil
		},
		resolveLocation: singleIdentifierLocationResolver(t),
		updateAccountContractCode: func(address Address, name string, code []byte) error {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			accountCodes[location.ID()] = code
			return nil
		},
		getAccountContractCode: func(address Address, name string) (code []byte, err error) {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			code = accountCodes[location.ID()]
			return code, nil
		},
		emitEvent: func(event cadence.Event) error {
			events = append(events, event)
			return nil
		},
		log: func(message string) {
			loggedMessages = append(loggedMessages, message)
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Deploy contracts

	for _, deployTx := range [][]byte{
		deployFTContractTx,
		deployDucContractTx,
		deployTestContractTx,
	} {

		err := runtime.ExecuteTransaction(
			Script{
				Source: deployTx,
			},
			Context{
				Interface: runtimeInterface,
				Location:  nextTransactionLocation(),
			},
		)
		require.NoError(t, err)

	}

	// Run test transaction

	const testTx = `
import TestContract from 0xaad3e26e406987c2
import DapperUtilityCoin from 0xaad3e26e406987c2

transaction {
  prepare(acct: AuthAccount) {

    let rc <- TestContract.createConverter()
    acct.save(<-rc, to: /storage/rc)

    acct.link<&TestContract.resourceConverter2>(/public/rc, target: /storage/rc)

    let optRef = getAccount(0xaad3e26e406987c2).getCapability(/public/rc).borrow<&TestContract.resourceConverter2>()

    if let ref = optRef {

      var tokens <- DapperUtilityCoin.createEmptyVault()

      var vaultx = ref.convert(b: <-tokens)

      acct.save(vaultx, to: /storage/v1)

      acct.link<&DapperUtilityCoin.Vault>(/public/v1, target: /storage/v1)

      var cap3 = getAccount(0xaad3e26e406987c2).getCapability(/public/v1).borrow<&DapperUtilityCoin.Vault>()!

      log(cap3.balance)
    } else {
      panic("failed to borrow resource converter")
    }
  }
}
`

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(testTx),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)

	require.Error(t, err)

	require.Contains(t, err.Error(), "failed to borrow resource converter")
}

func TestRuntimeStorageReadAndBorrow(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	storage := newTestLedger(nil, nil)

	signer := common.BytesToAddress([]byte{0x42})

	runtimeInterface := &testRuntimeInterface{
		storage: storage,
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signer}, nil
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Store a value and link a capability

	err := runtime.ExecuteTransaction(
		Script{
			Source: []byte(`
              transaction {
                 prepare(signer: AuthAccount) {
                     signer.save(42, to: /storage/test)
                     signer.link<&Int>(
                         /private/test,
                         target: /storage/test
                     )
                 }
              }
            `),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	t.Run("read stored, existing", func(t *testing.T) {

		value, err := runtime.ReadStored(
			signer,
			cadence.Path{
				Domain:     "storage",
				Identifier: "test",
			},
			Context{
				// NOTE: no location
				Interface: runtimeInterface,
			},
		)
		require.NoError(t, err)
		require.Equal(t, cadence.NewOptional(cadence.NewInt(42)), value)
	})

	t.Run("read stored, non-existing", func(t *testing.T) {

		value, err := runtime.ReadStored(
			signer,
			cadence.Path{
				Domain:     "storage",
				Identifier: "other",
			},
			Context{
				// NOTE: no location
				Interface: runtimeInterface,
			},
		)
		require.NoError(t, err)
		require.Equal(t, cadence.NewOptional(nil), value)
	})

	t.Run("read linked, existing", func(t *testing.T) {

		value, err := runtime.ReadLinked(
			signer,
			cadence.Path{
				Domain:     "private",
				Identifier: "test",
			},
			Context{
				Location:  utils.TestLocation,
				Interface: runtimeInterface,
			},
		)
		require.NoError(t, err)
		require.Equal(t, cadence.NewOptional(cadence.NewInt(42)), value)
	})

	t.Run("read linked, non-existing", func(t *testing.T) {

		value, err := runtime.ReadLinked(
			signer,
			cadence.Path{
				Domain:     "private",
				Identifier: "other",
			},
			Context{
				Location:  utils.TestLocation,
				Interface: runtimeInterface,
			},
		)
		require.NoError(t, err)
		require.Equal(t, cadence.NewOptional(nil), value)
	})
}

func TestRuntimeTopShotContractDeployment(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	testAddress, err := common.HexToAddress("0x0b2a3299cc857e29")
	require.NoError(t, err)

	nextTransactionLocation := newTransactionLocationGenerator()

	accountCodes := map[common.LocationID]string{
		"A.1d7e57aa55817448.NonFungibleToken": realNonFungibleTokenInterface,
	}

	events := make([]cadence.Event, 0)

	runtimeInterface := &testRuntimeInterface{
		storage: newTestLedger(nil, nil),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{testAddress}, nil
		},
		resolveLocation: singleIdentifierLocationResolver(t),
		updateAccountContractCode: func(address Address, name string, code []byte) error {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			accountCodes[location.ID()] = string(code)
			return nil
		},
		getAccountContractCode: func(address Address, name string) (code []byte, err error) {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			code = []byte(accountCodes[location.ID()])
			return code, nil
		},
		decodeArgument: func(b []byte, t cadence.Type) (cadence.Value, error) {
			return json.Decode(b)
		},
		emitEvent: func(event cadence.Event) error {
			events = append(events, event)
			return nil
		},
	}

	err = runtime.ExecuteTransaction(
		Script{
			Source: utils.DeploymentTransaction(
				"TopShot",
				[]byte(realTopShotContract),
			),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	err = runtime.ExecuteTransaction(
		Script{
			Source: utils.DeploymentTransaction(
				"TopShotShardedCollection",
				[]byte(realTopShotShardedCollectionContract),
			),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	err = runtime.ExecuteTransaction(
		Script{
			Source: utils.DeploymentTransaction(
				"TopshotAdminReceiver",
				[]byte(realTopshotAdminReceiverContract),
			),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)
}

func TestRuntimeTopShotBatchTransfer(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	accountCodes := map[common.LocationID]string{
		"A.1d7e57aa55817448.NonFungibleToken": realNonFungibleTokenInterface,
	}

	deployTx := utils.DeploymentTransaction("TopShot", []byte(realTopShotContract))

	topShotAddress, err := common.HexToAddress("0x0b2a3299cc857e29")
	require.NoError(t, err)

	var events []cadence.Event
	var loggedMessages []string

	var signerAddress common.Address

	var contractValueReads = 0

	onRead := func(owner, key, value []byte) {
		if bytes.Equal(key, []byte(formatContractKey("TopShot"))) {
			contractValueReads++
		}
	}

	runtimeInterface := &testRuntimeInterface{
		storage: newTestLedger(onRead, nil),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signerAddress}, nil
		},
		resolveLocation: singleIdentifierLocationResolver(t),
		updateAccountContractCode: func(address Address, name string, code []byte) error {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			accountCodes[location.ID()] = string(code)
			return nil
		},
		getAccountContractCode: func(address Address, name string) (code []byte, err error) {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			code = []byte(accountCodes[location.ID()])
			return code, nil
		},
		emitEvent: func(event cadence.Event) error {
			events = append(events, event)
			return nil
		},
		decodeArgument: func(b []byte, t cadence.Type) (cadence.Value, error) {
			return json.Decode(b)
		},
		log: func(message string) {
			loggedMessages = append(loggedMessages, message)
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Deploy TopShot contract

	signerAddress = topShotAddress

	err = runtime.ExecuteTransaction(
		Script{
			Source: deployTx,
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Mint moments

	contractValueReads = 0

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(`
              import TopShot from 0x0b2a3299cc857e29

              transaction {

                  prepare(signer: AuthAccount) {
                      let adminRef = signer.borrow<&TopShot.Admin>(from: /storage/TopShotAdmin)!

                      let playID = adminRef.createPlay(metadata: {"name": "Test"})
                      let setID = TopShot.nextSetID
                      adminRef.createSet(name: "Test")
                      let setRef = adminRef.borrowSet(setID: setID)
                      setRef.addPlay(playID: playID)

                      let moments <- setRef.batchMintMoment(playID: playID, quantity: 2)

                      signer.borrow<&TopShot.Collection>(from: /storage/MomentCollection)!
                          .batchDeposit(tokens: <-moments)
                  }
              }
            `),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)
	require.Equal(t, 1, contractValueReads)

	// Set up receiver

	const setupTx = `
      import NonFungibleToken from 0x1d7e57aa55817448
      import TopShot from 0x0b2a3299cc857e29

      transaction {

          prepare(signer: AuthAccount) {
              signer.save(
                 <-TopShot.createEmptyCollection(),
                 to: /storage/MomentCollection
              )
              signer.link<&TopShot.Collection>(
                 /public/MomentCollection,
                 target: /storage/MomentCollection
              )
          }
      }
    `

	signerAddress = common.BytesToAddress([]byte{0x42})

	contractValueReads = 0

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(setupTx),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)

	require.NoError(t, err)
	require.Equal(t, 1, contractValueReads)

	// Transfer

	signerAddress = topShotAddress

	const transferTx = `
      import NonFungibleToken from 0x1d7e57aa55817448
      import TopShot from 0x0b2a3299cc857e29

      transaction(momentIDs: [UInt64]) {
          let transferTokens: @NonFungibleToken.Collection

          prepare(acct: AuthAccount) {
              let ref = acct.borrow<&TopShot.Collection>(from: /storage/MomentCollection)!
              self.transferTokens <- ref.batchWithdraw(ids: momentIDs)
          }

          execute {
              // get the recipient's public account object
              let recipient = getAccount(0x42)

              // get the Collection reference for the receiver
              let receiverRef = recipient.getCapability(/public/MomentCollection)
                  .borrow<&{TopShot.MomentCollectionPublic}>()!

              // deposit the NFT in the receivers collection
              receiverRef.batchDeposit(tokens: <-self.transferTokens)
          }
      }
    `

	encodedArg, err := json.Encode(
		cadence.NewArray([]cadence.Value{
			cadence.NewUInt64(1),
		}),
	)
	require.NoError(t, err)

	contractValueReads = 0

	err = runtime.ExecuteTransaction(
		Script{
			Source:    []byte(transferTx),
			Arguments: [][]byte{encodedArg},
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)

	require.NoError(t, err)

	require.Equal(t, 0, contractValueReads)
}

func TestRuntimeBatchMintAndTransfer(t *testing.T) {

	if testing.Short() {
		t.Skip()
	}

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	const contract = `
      pub contract Test {

          pub resource interface INFT {}

          pub resource NFT: INFT {}

          pub resource Collection {

              pub var ownedNFTs: @{UInt64: NFT}

              init() {
                  self.ownedNFTs <- {}
              }

              pub fun withdraw(id: UInt64): @NFT {
                  let token <- self.ownedNFTs.remove(key: id)
                      ?? panic("Cannot withdraw: NFT does not exist in the collection")

                  return <-token
              }

              pub fun deposit(token: @NFT) {
                  let oldToken <- self.ownedNFTs[token.uuid] <- token
                  destroy oldToken
              }

              pub fun batchDeposit(collection: @Collection) {
                  let ids = collection.getIDs()

                  for id in ids {
                      self.deposit(token: <-collection.withdraw(id: id))
                  }

                  destroy collection
              }

              pub fun batchWithdraw(ids: [UInt64]): @Collection {
                  let collection <- create Collection()

                  for id in ids {
                      collection.deposit(token: <-self.withdraw(id: id))
                  }

                  return <-collection
              }

              pub fun getIDs(): [UInt64] {
                  return self.ownedNFTs.keys
              }

              destroy() {
                  destroy self.ownedNFTs
              }
          }

          init() {
              self.account.save(
                 <-Test.createEmptyCollection(),
                 to: /storage/MainCollection
              )
              self.account.link<&Collection>(
                 /public/MainCollection,
                 target: /storage/MainCollection
              )
          }

          pub fun mint(): @NFT {
              return <- create NFT()
          }

          pub fun createEmptyCollection(): @Collection {
              return <- create Collection()
          }

          pub fun batchMint(count: UInt64): @Collection {
              let collection <- create Collection()

              var i: UInt64 = 0
              while i < count {
                  collection.deposit(token: <-self.mint())
                  i = i + 1
              }
              return <-collection
          }
      }
    `

	deployTx := utils.DeploymentTransaction("Test", []byte(contract))

	contractAddress := common.BytesToAddress([]byte{0x1})

	var events []cadence.Event
	var loggedMessages []string

	var signerAddress common.Address

	accountCodes := map[common.LocationID]string{}

	var uuid uint64

	runtimeInterface := &testRuntimeInterface{
		generateUUID: func() (uint64, error) {
			uuid++
			return uuid, nil
		},
		storage: newTestLedger(nil, nil),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signerAddress}, nil
		},
		resolveLocation: singleIdentifierLocationResolver(t),
		updateAccountContractCode: func(address Address, name string, code []byte) error {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			accountCodes[location.ID()] = string(code)
			return nil
		},
		getAccountContractCode: func(address Address, name string) (code []byte, err error) {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			code = []byte(accountCodes[location.ID()])
			return code, nil
		},
		emitEvent: func(event cadence.Event) error {
			events = append(events, event)
			return nil
		},
		decodeArgument: func(b []byte, t cadence.Type) (cadence.Value, error) {
			return json.Decode(b)
		},
		log: func(message string) {
			loggedMessages = append(loggedMessages, message)
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Deploy contract

	signerAddress = contractAddress

	err := runtime.ExecuteTransaction(
		Script{
			Source: deployTx,
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Mint moments

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(`
              import Test from 0x1

              transaction {

                  prepare(signer: AuthAccount) {
                      let collection <- Test.batchMint(count: 1000)

                      log(collection.getIDs())

                      signer.borrow<&Test.Collection>(from: /storage/MainCollection)!
                          .batchDeposit(collection: <-collection)
                  }
              }
            `),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Set up receiver

	const setupTx = `
      import Test from 0x1

      transaction {

          prepare(signer: AuthAccount) {
              signer.save(
                 <-Test.createEmptyCollection(),
                 to: /storage/TestCollection
              )
              signer.link<&Test.Collection>(
                 /public/TestCollection,
                 target: /storage/TestCollection
              )
          }
      }
    `

	signerAddress = common.BytesToAddress([]byte{0x2})

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(setupTx),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)

	require.NoError(t, err)

	// Transfer

	signerAddress = contractAddress

	const transferTx = `
      import Test from 0x1

      transaction(ids: [UInt64]) {
          let collection: @Test.Collection

          prepare(signer: AuthAccount) {
              self.collection <- signer.borrow<&Test.Collection>(from: /storage/MainCollection)!
                  .batchWithdraw(ids: ids)
          }

          execute {
              getAccount(0x2)
                  .getCapability(/public/TestCollection)
                  .borrow<&Test.Collection>()!
                  .batchDeposit(collection: <-self.collection)
          }
      }
    `

	var values []cadence.Value

	const startID uint64 = 10
	const count = 20

	for id := startID; id <= startID+count; id++ {
		values = append(values, cadence.NewUInt64(id))
	}

	encodedArg, err := json.Encode(cadence.NewArray(values))
	require.NoError(t, err)

	err = runtime.ExecuteTransaction(
		Script{
			Source:    []byte(transferTx),
			Arguments: [][]byte{encodedArg},
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)
}

func TestRuntimeStorageUnlink(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	storage := newTestLedger(nil, nil)

	signer := common.BytesToAddress([]byte{0x42})

	runtimeInterface := &testRuntimeInterface{
		storage: storage,
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signer}, nil
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Store a value and link a capability

	err := runtime.ExecuteTransaction(
		Script{
			Source: []byte(`
              transaction {
                  prepare(signer: AuthAccount) {
                      signer.save(42, to: /storage/test)

                      signer.link<&Int>(
                          /public/test,
                          target: /storage/test
                      )

                      assert(signer.getCapability<&Int>(/public/test).borrow() != nil)
                  }
              }
            `),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Unlink the capability

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(`
            transaction {
                prepare(signer: AuthAccount) {
                    signer.unlink(/public/test)

                    assert(signer.getCapability<&Int>(/public/test).borrow() == nil)
                }
            }
            `),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Get the capability after unlink

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(`
              transaction {
                  prepare(signer: AuthAccount) {
                      assert(signer.getCapability<&Int>(/public/test).borrow() == nil)
                  }
              }
            `),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)
}

func TestRuntimeStorageSaveCapability(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	storage := newTestLedger(nil, nil)

	signer := common.BytesToAddress([]byte{0x42})

	runtimeInterface := &testRuntimeInterface{
		storage: storage,
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signer}, nil
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Store a capability

	for _, domain := range []common.PathDomain{
		common.PathDomainPrivate,
		common.PathDomainPublic,
	} {

		for typeDescription, ty := range map[string]cadence.Type{
			"Untyped": nil,
			"Typed":   cadence.ReferenceType{Authorized: false, Type: cadence.IntType{}},
		} {

			t.Run(fmt.Sprintf("%s %s", domain.Identifier(), typeDescription), func(t *testing.T) {

				storagePath := cadence.Path{
					Domain: "storage",
					Identifier: fmt.Sprintf(
						"test%s%s",
						typeDescription,
						domain.Identifier(),
					),
				}

				context := Context{
					Interface: runtimeInterface,
					Location:  nextTransactionLocation(),
				}

				var typeArgument string
				if ty != nil {
					typeArgument = fmt.Sprintf("<%s>", ty.ID())
				}

				err := runtime.ExecuteTransaction(
					Script{
						Source: []byte(fmt.Sprintf(
							`
                              transaction {
                                  prepare(signer: AuthAccount) {
                                      let cap = signer.getCapability%s(/%s/test)
                                      signer.save(cap, to: %s)
                                  }
                              }
                            `,
							typeArgument,
							domain.Identifier(),
							storagePath,
						)),
					},
					context,
				)
				require.NoError(t, err)

				value, err := runtime.ReadStored(signer, storagePath, context)
				require.NoError(t, err)

				require.Equal(t,
					cadence.Optional{
						Value: cadence.Capability{
							Path: cadence.Path{
								Domain:     domain.Identifier(),
								Identifier: "test",
							},
							Address:    cadence.Address(signer),
							BorrowType: ty,
						},
					},
					value,
				)
			})
		}
	}
}

func TestRuntimeStorageReferenceCast(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	signerAddress := common.BytesToAddress([]byte{0x42})

	deployTx := utils.DeploymentTransaction("Test", []byte(`
      pub contract Test {

          pub resource interface RI {}

          pub resource R: RI {}

          pub fun createR(): @R {
              return <-create R()
          }
      }
    `))

	accountCodes := map[common.LocationID][]byte{}
	var events []cadence.Event
	var loggedMessages []string

	runtimeInterface := &testRuntimeInterface{
		storage: newTestLedger(nil, nil),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signerAddress}, nil
		},
		resolveLocation: singleIdentifierLocationResolver(t),
		updateAccountContractCode: func(address Address, name string, code []byte) error {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			accountCodes[location.ID()] = code
			return nil
		},
		getAccountContractCode: func(address Address, name string) (code []byte, err error) {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			code = accountCodes[location.ID()]
			return code, nil
		},
		emitEvent: func(event cadence.Event) error {
			events = append(events, event)
			return nil
		},
		log: func(message string) {
			loggedMessages = append(loggedMessages, message)
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Deploy contract

	err := runtime.ExecuteTransaction(
		Script{
			Source: deployTx,
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Run test transaction

	const testTx = `
      import Test from 0x42

      transaction {
          prepare(signer: AuthAccount) {
              signer.save(<-Test.createR(), to: /storage/r)

              signer.link<&Test.R{Test.RI}>(
                 /public/r,
                 target: /storage/r
              )

              let ref = signer.getCapability<&Test.R{Test.RI}>(/public/r).borrow()!

              let casted = (ref as AnyStruct) as! &Test.R
          }
      }
    `

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(testTx),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)

	require.Error(t, err)

	require.Contains(t, err.Error(), "unexpectedly found non-`&Test.R` while force-casting value")
}

func TestRuntimeStorageNonStorable(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	address := common.BytesToAddress([]byte{0x1})

	for name, code := range map[string]string{
		"ephemeral reference": `
            let value = &1 as &Int
        `,
		"storage reference": `
            signer.save("test", to: /storage/string)
            let value = signer.borrow<&String>(from: /storage/string)!
        `,
		"function": `
            let value = fun () {}
        `,
	} {

		t.Run(name, func(t *testing.T) {

			tx := []byte(
				fmt.Sprintf(
					`
                      transaction {
                          prepare(signer: AuthAccount) {
                              %s
                              signer.save((value as AnyStruct), to: /storage/value)
                          }
                       }
                    `,
					code,
				),
			)

			runtimeInterface := &testRuntimeInterface{
				storage: newTestLedger(nil, nil),
				getSigningAccounts: func() ([]Address, error) {
					return []Address{address}, nil
				},
			}

			nextTransactionLocation := newTransactionLocationGenerator()

			err := runtime.ExecuteTransaction(
				Script{
					Source: tx,
				},
				Context{
					Interface: runtimeInterface,
					Location:  nextTransactionLocation(),
				},
			)
			require.Error(t, err)

			require.Contains(t, err.Error(), "cannot store non-storable value")
		})
	}
}

func TestRuntimeStorageRecursiveReference(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	address := common.BytesToAddress([]byte{0x1})

	const code = `
      transaction {
          prepare(signer: AuthAccount) {
              let refs: [AnyStruct] = []
              refs.insert(at: 0, &refs as &AnyStruct)
              signer.save(refs, to: /storage/refs)
          }
      }
    `

	runtimeInterface := &testRuntimeInterface{
		storage: newTestLedger(nil, nil),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{address}, nil
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	err := runtime.ExecuteTransaction(
		Script{
			Source: []byte(code),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.Error(t, err)

	require.Contains(t, err.Error(), "cannot store non-storable value")
}

func TestRuntimeStorageTransfer(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	address1 := common.BytesToAddress([]byte{0x1})
	address2 := common.BytesToAddress([]byte{0x2})

	ledger := newTestLedger(nil, nil)

	var signers []Address

	runtimeInterface := &testRuntimeInterface{
		storage: ledger,
		getSigningAccounts: func() ([]Address, error) {
			return signers, nil
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Store

	signers = []Address{address1}

	storeTx := []byte(`
      transaction {
          prepare(signer: AuthAccount) {
              signer.save([1], to: /storage/test)
          }
       }
    `)

	err := runtime.ExecuteTransaction(
		Script{
			Source: storeTx,
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Transfer

	signers = []Address{address1, address2}

	transferTx := []byte(`
      transaction {
          prepare(signer1: AuthAccount, signer2: AuthAccount) {
              let value = signer1.load<[Int]>(from: /storage/test)!
              signer2.save(value, to: /storage/test)
          }
       }
    `)

	err = runtime.ExecuteTransaction(
		Script{
			Source: transferTx,
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	var nonEmptyKeys int
	for _, data := range ledger.storedValues {
		if len(data) > 0 {
			nonEmptyKeys++
		}
	}
	assert.Equal(t, 2, nonEmptyKeys)
}

func TestRuntimeStorageUsed(t *testing.T) {

	t.Parallel()

	runtime := newTestInterpreterRuntime()

	ledger := newTestLedger(nil, nil)

	runtimeInterface := &testRuntimeInterface{
		storage: ledger,
		getStorageUsed: func(_ Address) (uint64, error) {
			return 1, nil
		},
	}

	// NOTE: do NOT change the contents of this script,
	// it matters how the array is constructed,
	// ESPECIALLY the value of the addresses and the number of elements!
	//
	// Querying storageUsed commits storage, and this test asserts
	// that this should not clear temporary slabs

	script := []byte(`
       pub fun main() {
            var addresses: [Address]= [
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731,
                0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731, 0x2a3c4c2581cef731
            ]
            var count = 0
            for address in addresses {
                let account = getAccount(address)
                var x = account.storageUsed
            }
        }
    `)

	_, err := runtime.ExecuteScript(
		Script{
			Source: script,
		},
		Context{
			Interface: runtimeInterface,
			Location:  common.ScriptLocation{},
		},
	)
	require.NoError(t, err)

}

func TestSortAccountStorageEntries(t *testing.T) {

	t.Parallel()

	entries := []AccountStorageEntry{
		{
			StorageKey: interpreter.StorageKey{
				Address: common.Address{2},
				Key:     "a",
			},
		},
		{
			StorageKey: interpreter.StorageKey{
				Address: common.Address{1},
				Key:     "b",
			},
		},
		{
			StorageKey: interpreter.StorageKey{
				Address: common.Address{1},
				Key:     "a",
			},
		},
		{
			StorageKey: interpreter.StorageKey{
				Address: common.Address{0},
				Key:     "x",
			},
		},
	}

	SortAccountStorageEntries(entries)

	require.Equal(t,
		[]AccountStorageEntry{
			{
				StorageKey: interpreter.StorageKey{
					Address: common.Address{0},
					Key:     "x",
				},
			},
			{
				StorageKey: interpreter.StorageKey{
					Address: common.Address{1},
					Key:     "a",
				},
			},
			{
				StorageKey: interpreter.StorageKey{
					Address: common.Address{1},
					Key:     "b",
				},
			},
			{
				StorageKey: interpreter.StorageKey{
					Address: common.Address{2},
					Key:     "a",
				},
			},
		},
		entries,
	)
}

func TestRuntimeMissingSlab1173(t *testing.T) {

	t.Parallel()

	const contract = `
pub contract Test {
    pub enum Role: UInt8 {
        pub case aaa
        pub case bbb
    }

    pub resource AAA {
        pub fun callA(): String {
            return "AAA"
        }
    }

    pub resource BBB {
        pub fun callB(): String {
            return "BBB"
        }
    }

    pub resource interface Receiver {
        pub fun receive(as: Role, capability: Capability)
    }

    pub resource Holder: Receiver {
        access(self) let roles: { Role: Capability }
        pub fun receive(as: Role, capability: Capability) {
            self.roles[as] = capability
        }

        pub fun borrowA(): &AAA {
            let role = self.roles[Role.aaa]!
            return role.borrow<&AAA>()!
        }

        pub fun borrowB(): &BBB {
            let role = self.roles[Role.bbb]!
            return role.borrow<&BBB>()!
        }

        access(contract) init() {
            self.roles = {}
        }
    }

    access(self) let capabilities: { Role: Capability }

    pub fun createHolder(): @Holder {
        return <- create Holder()
    }

    pub fun attach(as: Role, receiver: &AnyResource{Receiver}) {
        // TODO: Now verify that the owner is valid.

        let capability = self.capabilities[as]!
        receiver.receive(as: as, capability: capability)
    }

    init() {
        self.account.save<@AAA>(<- create AAA(), to: /storage/TestAAA)
        self.account.save<@BBB>(<- create BBB(), to: /storage/TestBBB)

        self.capabilities = {}
        self.capabilities[Role.aaa] = self.account.link<&AAA>(/private/TestAAA, target: /storage/TestAAA)!
        self.capabilities[Role.bbb] = self.account.link<&BBB>(/private/TestBBB, target: /storage/TestBBB)!
    }
}

`

	const tx = `
import Test from 0x1

transaction {
    prepare(acct: AuthAccount) {}
    execute {
        let holder <- Test.createHolder()
        Test.attach(as: Test.Role.aaa, receiver: &holder as &AnyResource{Test.Receiver})
        destroy holder
    }
}
`

	runtime := newTestInterpreterRuntime()

	testAddress := common.BytesToAddress([]byte{0x1})

	accountCodes := map[common.LocationID][]byte{}

	var events []cadence.Event

	signerAccount := testAddress

	runtimeInterface := &testRuntimeInterface{
		getCode: func(location Location) (bytes []byte, err error) {
			return accountCodes[location.ID()], nil
		},
		storage: newTestLedger(nil, nil),
		getSigningAccounts: func() ([]Address, error) {
			return []Address{signerAccount}, nil
		},
		resolveLocation: singleIdentifierLocationResolver(t),
		getAccountContractCode: func(address Address, name string) (code []byte, err error) {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			return accountCodes[location.ID()], nil
		},
		updateAccountContractCode: func(address Address, name string, code []byte) error {
			location := common.AddressLocation{
				Address: address,
				Name:    name,
			}
			accountCodes[location.ID()] = code
			return nil
		},
		emitEvent: func(event cadence.Event) error {
			events = append(events, event)
			return nil
		},
		decodeArgument: func(b []byte, t cadence.Type) (value cadence.Value, err error) {
			return json.Decode(b)
		},
	}

	nextTransactionLocation := newTransactionLocationGenerator()

	// Deploy contract

	err := runtime.ExecuteTransaction(
		Script{
			Source: utils.DeploymentTransaction(
				"Test",
				[]byte(contract),
			),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)

	// Run transaction

	err = runtime.ExecuteTransaction(
		Script{
			Source: []byte(tx),
		},
		Context{
			Interface: runtimeInterface,
			Location:  nextTransactionLocation(),
		},
	)
	require.NoError(t, err)
}
