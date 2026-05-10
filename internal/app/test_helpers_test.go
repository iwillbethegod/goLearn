package app_test

import "os"

func osGetwd() (string, error) { return os.Getwd() }
func osChdir(s string) error   { return os.Chdir(s) }

func init() {
	// Force `_ = context` etc. — none needed; placeholder so the file
	// has a side-effect import we may add later.
}
