package console

import (
	"fmt"
	"strings"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// MigrateStatus displays the status of all registered migrations.
func MigrateStatus(db *orm.Manager) error {
	if db == nil {
		fmt.Println("No database configured")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		fmt.Println("No migrations found")
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

	// Calculate column widths
	versionWidth := len("Version")
	descWidth := len("Description")
	statusWidth := len("Status")
	batchWidth := len("Batch")

	for _, s := range statuses {
		if len(s.Version) > versionWidth {
			versionWidth = len(s.Version)
		}
		desc := descriptionMap[s.Version]
		if len(desc) > descWidth {
			descWidth = len(desc)
		}
		if len(s.State) > statusWidth {
			statusWidth = len(s.State)
		}
	}

	versionWidth += 3
	descWidth += 3
	statusWidth += 3
	batchWidth += 3

	// Header
	fmt.Println()
	fmt.Printf("  %-*s %-*s %-*s %-*s\n", versionWidth, "Version", descWidth, "Description", statusWidth, "Status", batchWidth, "Batch")
	fmt.Printf("  %s\n", strings.Repeat("─", versionWidth+descWidth+statusWidth+batchWidth))

	// Rows
	for _, s := range statuses {
		desc := descriptionMap[s.Version]
		status := "Pending"
		batchStr := ""
		if s.State == "Applied" {
			status = "Ran"
			batchStr = fmt.Sprintf("%d", s.Batch)
		}
		fmt.Printf("  %-*s %-*s %-*s %-*s\n", versionWidth, s.Version, descWidth, desc, statusWidth, status, batchWidth, batchStr)
	}

	fmt.Println()
	return nil
}
