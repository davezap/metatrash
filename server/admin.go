package server

import (
	"context"
	"metatrash.com/metatrash/internal/service"
)

type AdminDB = service.AdminDB
type AdminUser = service.AdminUser

func OpenAdminDB(ctx context.Context, path string) (*AdminDB, error) {
	return service.OpenAdminDB(ctx, path)
}
