# Ledger: Overview

## Problem

A digital wallet (think Nequi or Cash App) holds people's money as records in a database, so a bug, a retried request or a crashed process can create, lose or duplicate it.
Ledger is the backend core of such a wallet: user and merchant wallets, P2P transfers, merchant payments with fees, holds, refunds, cash-in and cash-out, and signed webhooks.
It uses double-entry bookkeeping: money only moves from one account to another, so every transaction sums to zero and the books can always be checked.

## Goals

Ledger guarantees that money is never:

- **Created:** every movement sums to zero, so no balance grows unless another one shrinks.
- **Lost:** a movement is applied completely or not at all, even if a process dies halfway.
- **Applied twice:** repeating a request does not repeat its effect.

These guarantees must hold under real-world failure: high concurrency, duplicated requests and workers that die mid-operation.

## Non-goals

- **Real money:** all funds, banks and providers are simulated.
- **Real KYC:** there is no real identity verification.
- **Frontend:** Ledger is a backend exposed through an API.
- **Microservices:** it is one codebase with two processes, an API and a worker.
- **Cards:** there is no card issuing or card network processing.

## Scope of v0.1

- **Accounts:** create the accounts that hold money.
- **Journal entries:** record movements as immutable, balanced transactions.
- **Balances:** read an account's current balance.
- **Idempotent P2P transfer:** move money between two users, safely retryable.
- **History:** list an account's past movements.

Merchant payments, holds, refunds, cash-in, cash-out and webhooks come after v0.1.

## Actors

- **User:** holds a wallet and sends or receives money.
- **Merchant:** a business that holds a wallet and is paid by users.
- **Administrator:** operates the platform and supervises its accounts and books.
- **External provider (simulated):** stands in for the bank or network that moves money into and out of Ledger.

## Glossary

- **Account:** a place that holds money, owned by a user, a merchant or the platform.
- **System account:** an account owned by the platform, such as collected fees, so that every movement has two sides.
- **Journal transaction:** one immutable money movement, made of postings that sum to zero and applied all at once.
- **Posting:** one line of a journal transaction: a signed amount on one account, negative for money out and positive for money in.
- **Balance projection:** a stored balance kept for fast reads, which can always be rebuilt from the postings.
- **Available/locked balance:** available money can be spent now; locked money still belongs to the owner but is reserved by a hold.
- **Hold:** a temporary reservation of funds that ends in a capture (the money is taken) or a void (the money is released).
- **Reference:** a unique identifier for a money movement, which stops the same movement from being recorded twice.
- **Idempotency key:** a unique value the client sends with a request, so a retry returns the original result instead of running again.
