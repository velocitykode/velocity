package console

import (
	"strings"

	"github.com/velocitykode/velocity/console/scaffold"
)

// MakeJobOptions holds flags for the make:job command.
type MakeJobOptions struct {
	Dir string // --dir output directory override (default internal/jobs)
}

// MakeJob generates a new queue job file from a stub template.
func MakeJob(name string, opts MakeJobOptions) error {
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
