package domain

import (
	"encoding/json"
	"github.com/google/uuid"
	"time"
)

// SavedSearch stores a versioned catalogue query. Notification state is reserved
// for a future opt-in workflow; creating a search does not schedule delivery.
type SavedSearch struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Name      string
	Query     map[string]json.RawMessage
	CreatedAt time.Time
}
