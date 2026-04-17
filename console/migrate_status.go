package console

import (
	"fmt"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// MigrateStatus displays the status of all registered migrations.
func MigrateStatus(db orm.Database) error {
	if db == nil {
		cli.Warning("No database configured")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		cli.Warning("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	statuses, err := migrator.Status()
	if err != nil {
		return fmt.Errorf("failed to get migration status: %w", err)
	}

	// Build a description lookup from registered migrations
	descriptionMap := make(map[string]string, len(migrations))
	for _, m := range migrations {
		descriptionMap[m.Version] = m.Description
	}

	headers := []string{"Version", "Description", "Status", "Batch"}
	var rows [][]string
	for _, s := range statuses {
		desc := descriptionMap[s.Version]
		status := "Pending"
		batchStr := ""
		if s.State == "Applied" {
			status = "Ran"
			batchStr = fmt.Sprintf("%d", s.Batch)
		}
		rows = append(rows, []string{s.Version, desc, status, batchStr})
	}

	cli.Table(headers, rows)
	return nil
}
