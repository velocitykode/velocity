package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// GenJobOptions holds flags for the gen job command.
type GenJobOptions struct {
	Dir string // --dir output directory override (default internal/jobs)
}

// GenJob generates a new queue job file from a stub template.
func GenJob(name string, opts GenJobOptions) error {
	if err := scaffold.ValidateName(name); err != nil {
		return err
	}

	jobName := toJobName(name)

	data := map[string]interface{}{
		"Package": "jobs",
		"Name":    jobName,
	}

	return writeScaffoldedFile(name, opts.Dir, "internal/jobs", "job", toSnakeCase(jobName)+".go", "internal/jobs/job.go.stub", data)
}

func toJobName(name string) string {
	name = strings.TrimSuffix(name, "Job")
	name = strings.TrimSuffix(name, "job")
	return toPascalCase(name)
}
