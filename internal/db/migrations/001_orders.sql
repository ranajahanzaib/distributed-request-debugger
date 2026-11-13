CREATE TABLE IF NOT EXISTS orders.orders (
 id TEXT PRIMARY KEY,
 customer_id TEXT NOT NULL,
 sku TEXT NOT NULL,
 quantity INT NOT NULL CHECK(quantity > 0),
 status TEXT NOT NULL,
 idempotency_key TEXT UNIQUE NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
