package velocity_test

import (
	"fmt"
	"os"

	"github.com/velocitykode/velocity"
)

// An application reads its own keys with the framework's rules, in one
// place at boot, and stops on the first Err.
func ExampleEnvReader() {
	os.Setenv("EXAMPLE_PAGE_SIZE", "50")
	os.Setenv("EXAMPLE_BETA", "on")
	defer os.Unsetenv("EXAMPLE_PAGE_SIZE")
	defer os.Unsetenv("EXAMPLE_BETA")

	r := velocity.NewEnvReader()
	pageSize := r.Int("EXAMPLE_PAGE_SIZE", 20)
	beta := r.Bool("EXAMPLE_BETA", false)
	if err := r.Err(); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(pageSize, beta)
	// Output: 50 true
}
