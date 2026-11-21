-- People, customer accounts and companies: three separate things, never
-- mixed. A person is one human; a customer account (record type
-- customer_account, built in) is their workspace in the brand's own
-- product, with its plan, usage and billing and its members, each with a
-- role; a company is the organisation. A person can be in many accounts,
-- an account links to one company or none, and being in an account never
-- sets a person's company.

-- How a brand sets up its People: who it sells to (consumers, companies,
-- mixed or marketplace) and whether its product has accounts. Empty keys
-- take the default for the brand's business model (engine/internal/api/
-- people_setup.go), so every brand starts with smart defaults and can
-- change each one.
ALTER TABLE brands ADD COLUMN people_setup jsonb NOT NULL DEFAULT '{}';

-- One preferred email per person: sends go there. Unset, it is their most
-- recent verified address. An opt-out still covers every address they have.
ALTER TABLE record_identities ADD COLUMN preferred boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX record_identities_preferred_idx ON record_identities (record_id)
    WHERE preferred AND namespace = 'email' AND valid_to IS NULL;
