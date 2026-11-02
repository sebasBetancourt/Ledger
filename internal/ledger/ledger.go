// Package ledger records money movements as immutable, balanced transactions.
//
// Post validates a request, locks the affected balances in account order, and
// writes the transaction, its postings and the new balances in one database
// transaction. The database checks the same rules again at COMMIT.
package ledger
