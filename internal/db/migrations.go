package db

import (
	"context"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

func MigrateOrders(ctx context.Context, p *pgxpool.Pool) error {
	data, err := migrations.ReadFile("migrations/001_orders.sql")
	if err != nil {
		return err
	}
	_, err = p.Exec(ctx, string(data))
	return err
}
func MigrateInventory(ctx context.Context, p *pgxpool.Pool) error {
	data, err := migrations.ReadFile("migrations/002_inventory.sql")
	if err != nil {
		return err
	}
	_, err = p.Exec(ctx, string(data))
	return err
}
