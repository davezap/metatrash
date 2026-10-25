//go:build !unix

package service

// readableBy cannot tell outside Unix and answers true.
func readableBy(path, account string) (bool, error) { return true, nil }
