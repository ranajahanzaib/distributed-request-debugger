CREATE TABLE IF NOT EXISTS inventory.stock (
 sku TEXT PRIMARY KEY,
 available INT NOT NULL CHECK(available >= 0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO inventory.stock(sku,available)
VALUES ('SKU-001',100),('SKU-002',25) ON CONFLICT(sku) DO NOTHING;
CREATE TABLE IF NOT EXISTS inventory.reservations (
 idempotency_key TEXT PRIMARY KEY,
 sku TEXT NOT NULL,
 quantity INT NOT NULL CHECK(quantity > 0),
 status TEXT NOT NULL CHECK(status IN ('reserved','released')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
GRANT SELECT, UPDATE ON inventory.stock TO debugger;
