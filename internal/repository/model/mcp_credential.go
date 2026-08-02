package model

import "time"

// MCPCredential holds what an OAuth-protected MCP server needs to be reached
// again without sending the user back through the browser.
//
// Two different things live in one row because they are acquired together and
// are useless apart: the OAuth token, and the client identity that token was
// issued to. Dynamic client registration happens once, and if its result were
// not persisted every restart would register a brand-new client with the
// authorization server — leaving a trail of orphaned registrations and
// forcing a fresh consent screen each time.
//
// Keyed by server name, which is the key in mcp.json. Renaming a server there
// therefore drops its authorization, which is the right outcome: the name is
// how the user refers to the thing they consented to.
type MCPCredential struct {
	ServerName string `gorm:"primaryKey;size:128"`

	// ClientID / ClientSecret are either configured by hand or filled in by
	// dynamic registration. Empty ClientID means registration has not run.
	ClientID     string `gorm:"size:256"`
	ClientSecret string `gorm:"size:512"`

	// Token is a marshalled transport.Token. Stored as JSON rather than
	// columns because it is the library's struct, not ours, and mirroring its
	// fields would mean a migration every time the library adds one.
	Token string `gorm:"type:text"`

	UpdatedAt time.Time
}

func (MCPCredential) TableName() string {
	return "mcp_credentials"
}
