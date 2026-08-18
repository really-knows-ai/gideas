package flow

import "context"

// embassyTestTxID is the fake transaction ID bound to the mock transactions
// below. Historically defined alongside the embassy server tests; kept here
// because the transaction tests are its only remaining consumers.
const embassyTestTxID = "tx-1"

func newMockTx(mock *mockCartographerClient) *Transaction {
	return newMockTxWithID(mock, embassyTestTxID)
}

// newMockTxWithID returns a Transaction bound to the mock with the given
// transaction ID. An empty ID simulates a write issued without an active
// transaction — the wire carries no transactionId, and the Cartographer
// rejects it (FAILED_PRECONDITION "No active transaction").
func newMockTxWithID(mock *mockCartographerClient, txID string) *Transaction {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		Cartographer: mock,
		ctx:          ctx,
		cancel:       cancel,
	}
	return &Transaction{
		session:   sess,
		id:        txID,
		idTypeMap: newIDTypeMap(),
	}
}
