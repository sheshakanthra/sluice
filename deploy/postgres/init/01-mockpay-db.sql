-- Runs once, on first initialisation of the Postgres volume.
-- MockPay keeps its ledger in a separate database on the same server.
CREATE DATABASE mockpay OWNER sluice;
