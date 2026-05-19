package metabase

import (
	"context"
	"fmt"
)

// Metadata возвращает сырую метаинформацию о базе:
// таблицы, поля, FK. Используется для построения lean-схемы.
func (c *Client) Metadata(ctx context.Context, databaseID int) (*MetadataRaw, error) {
	var out MetadataRaw
	path := fmt.Sprintf("/api/database/%d/metadata", databaseID)
	if err := c.doJSON(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
