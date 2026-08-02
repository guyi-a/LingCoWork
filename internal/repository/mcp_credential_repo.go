package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type MCPCredentialRepo struct {
	db *gorm.DB
}

func NewMCPCredentialRepo(db *gorm.DB) *MCPCredentialRepo {
	return &MCPCredentialRepo{db: db}
}

// Get returns nil, nil when the server has no stored credential — the normal
// state for a server that has never been authorized.
func (r *MCPCredentialRepo) Get(ctx context.Context, server string) (*model.MCPCredential, error) {
	var row model.MCPCredential
	err := r.db.WithContext(ctx).Where("server_name = ?", server).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SaveToken writes only the token, leaving the client identity alone.
//
// Split from SaveClient because the two change on different schedules: the
// token is rewritten on every refresh, the client registration once ever. A
// single Upsert of the whole row would let a refresh racing a registration
// blank out the client id.
func (r *MCPCredentialRepo) SaveToken(ctx context.Context, server, token string) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "server_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"token", "updated_at"}),
	}).Create(&model.MCPCredential{ServerName: server, Token: token}).Error
}

func (r *MCPCredentialRepo) SaveClient(ctx context.Context, server, clientID, clientSecret string) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "server_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"client_id", "client_secret", "updated_at"}),
	}).Create(&model.MCPCredential{
		ServerName:   server,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}).Error
}

// Delete revokes locally: the next connection attempt will need a fresh
// authorization. It does not tell the server anything.
func (r *MCPCredentialRepo) Delete(ctx context.Context, server string) error {
	return r.db.WithContext(ctx).
		Where("server_name = ?", server).
		Delete(&model.MCPCredential{}).Error
}
